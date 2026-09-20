package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// LongXia uses an OpenAI API-key account and the existing Ark task entrance.
// Its task IDs are namespaced so they cannot collide with native Ark tasks.
const AccountExtraLongXia = "longxia_enabled"
const longXiaTaskPrefix = "longxia:"

func (a *Account) IsLongXia() bool {
	return a != nil && a.Platform == PlatformOpenAI && a.Type == AccountTypeAPIKey && a.getExtraBool(AccountExtraLongXia)
}

func IsLongXiaTask(id string) bool {
	return strings.HasPrefix(id, "seedance:"+longXiaTaskPrefix)
}

type longXiaAsset struct {
	Category string `json:"category"`
	URL      string `json:"url,omitempty"`
	Base64   string `json:"data_base64,omitempty"`
}

type longXiaRequest struct {
	Model    string         `json:"model"`
	Prompt   string         `json:"prompt"`
	Size     string         `json:"size"`
	Duration int            `json:"duration"`
	Assets   []longXiaAsset `json:"assets,omitempty"`
}

type longXiaCapabilities struct {
	resolution                            string
	minDuration, maxDuration, promptLimit int
	minImages, images, audios, videos     int
	audio                                 bool
	wideRatios                            bool
}

// The supplied public contract is inconsistent about "two" reference-video
// models. Only the explicitly named reference-video SKU is enabled here.
func longXiaModelCapabilities(model string) (longXiaCapabilities, bool) {
	c := longXiaCapabilities{resolution: "720p", minDuration: 4, maxDuration: 15, promptLimit: 5000, images: 6, audios: 2, audio: true}
	switch model {
	case "LongXia-video-seedance2_0-mini-express", "LongXia-video-seedance2_0-fast-express", "LongXia-video-seedance2_0-standard-express", "LongXia-video-seedance2_0-standard-express-PerSecond":
	case "LongXia-video-seedance2_5-standard-480p-express", "LongXia-video-seedance2_5-standard-480p-express-PerSecond", "LongXia-video-seedance2_5-standard-720p-express", "LongXia-video-seedance2_5-standard-720p-express-PerSecond", "LongXia-video-seedance2_5-standard-720p-reference-video-express":
		c.maxDuration, c.promptLimit, c.images, c.audios, c.wideRatios = 25, 9500, 30, 10, true
		if strings.Contains(model, "480p") {
			c.resolution = "480p"
		}
		if strings.Contains(model, "reference-video") {
			c.videos, c.maxDuration = 10, 15
		}
	case "LongXia-video-minimax-h3-express":
		c.resolution, c.minDuration, c.promptLimit, c.images, c.audios, c.wideRatios = "1440p", 5, 2000, 5, 3, true
	case "LongXia-video-gemini-omni-flash-express":
		c.minDuration, c.maxDuration, c.images, c.minImages, c.audios, c.audio = 3, 10, 5, 1, 0, false
	default:
		return c, false
	}
	return c, true
}

var longXiaReferencePattern = regexp.MustCompile(`@(image|audio|video)([0-9]+)\b`)

func parseLongXiaRequest(body []byte, model string) (longXiaRequest, longXiaCapabilities, error) {
	r := longXiaRequest{Model: model, Size: "9:16", Duration: 8}
	caps, known := longXiaModelCapabilities(model)
	invalid := func(message string) (longXiaRequest, longXiaCapabilities, error) {
		return r, caps, fmt.Errorf("LongXia: %s", message)
	}
	if !known {
		return invalid("unsupported model; configure a full public LongXia model ID")
	}
	if _, err := ParseSeedanceRequest(body); err != nil {
		return r, caps, err
	}
	root := gjson.ParseBytes(body)
	allowed := map[string]bool{"model": true, "content": true, "duration": true, "ratio": true, "resolution": true, "generate_audio": true}
	var fieldErr string
	root.ForEach(func(k, v gjson.Result) bool {
		if !allowed[k.String()] {
			fieldErr = "unsupported field: " + k.String()
			return false
		}
		return true
	})
	if fieldErr != "" {
		return invalid(fieldErr)
	}
	if d := root.Get("duration"); d.Exists() {
		if d.Type != gjson.Number || d.Float() != float64(d.Int()) || d.Int() < int64(caps.minDuration) || d.Int() > int64(caps.maxDuration) {
			return invalid(fmt.Sprintf("duration must be an integer from %d to %d", caps.minDuration, caps.maxDuration))
		}
		r.Duration = int(d.Int())
	}
	if ratio := root.Get("ratio"); ratio.Exists() {
		if ratio.Type != gjson.String {
			return invalid("ratio must be a string")
		}
		r.Size = ratio.String()
	}
	if r.Size != "9:16" && r.Size != "16:9" && !(caps.wideRatios && (r.Size == "21:9" || r.Size == "4:3" || r.Size == "1:1" || r.Size == "3:4")) {
		return invalid("unsupported aspect ratio for this model")
	}
	if res := root.Get("resolution"); res.Exists() && (res.Type != gjson.String || res.String() != caps.resolution) {
		return invalid("model has a fixed resolution of " + caps.resolution)
	}
	if audio := root.Get("generate_audio"); audio.Exists() && ((audio.Type != gjson.True && audio.Type != gjson.False) || audio.Bool() != caps.audio) {
		return invalid("model has a fixed audio output setting")
	}
	var texts []string
	counts := map[string]int{}
	totalBase64 := 0
	for _, item := range root.Get("content").Array() {
		if !item.IsObject() {
			return invalid("content items must be objects")
		}
		kind := item.Get("type").String()
		item.ForEach(func(k, v gjson.Result) bool {
			name := k.String()
			if name != "type" && !(kind == "text" && name == "text") && !(kind != "text" && (name == kind || name == "role")) {
				fieldErr = "unsupported content field: " + name
				return false
			}
			return true
		})
		if fieldErr != "" {
			return invalid(fieldErr)
		}
		if kind == "text" {
			if item.Get("text").Type != gjson.String {
				return invalid("content text must be a string")
			}
			texts = append(texts, item.Get("text").String())
			continue
		}
		category := strings.TrimSuffix(kind, "_url")
		if kind != "image_url" && kind != "audio_url" && kind != "video_url" {
			return invalid("unsupported content type: " + kind)
		}
		role := item.Get("role")
		if role.Exists() && (role.Type != gjson.String || role.String() != "reference_"+category) {
			return invalid("only reference media roles are supported; first/last frame controls are unavailable")
		}
		source := item.Get(kind + ".url")
		item.Get(kind).ForEach(func(k, v gjson.Result) bool {
			if k.String() != "url" {
				fieldErr = "reference objects only support url"
				return false
			}
			return true
		})
		if fieldErr != "" {
			return invalid(fieldErr)
		}
		if source.Type != gjson.String {
			return invalid("reference URL must be a string")
		}
		asset, err := longXiaReference(category, source.String())
		if err != nil {
			return r, caps, err
		}
		totalBase64 += len(asset.Base64)
		if totalBase64 > 40<<20 {
			return invalid("reference Base64 total exceeds 40 MiB")
		}
		r.Assets = append(r.Assets, asset)
		counts[category]++
	}
	r.Prompt = strings.TrimSpace(strings.Join(texts, "\n"))
	if r.Prompt == "" || utf8.RuneCountInString(r.Prompt) > caps.promptLimit {
		return invalid(fmt.Sprintf("prompt must contain 1–%d characters", caps.promptLimit))
	}
	if counts["image"] < caps.minImages || counts["image"] > caps.images || counts["audio"] > caps.audios || counts["video"] > caps.videos {
		return invalid("reference counts or media combination are not supported by this model")
	}
	if strings.Contains(model, "minimax-h3") && counts["audio"] > 0 && counts["image"] == 0 {
		return invalid("H3 audio references require an image")
	}
	seen := map[string]map[int]bool{"image": {}, "audio": {}, "video": {}}
	for _, match := range longXiaReferencePattern.FindAllStringSubmatch(r.Prompt, -1) {
		n, err := strconv.Atoi(match[2])
		if err != nil || n < 1 || n > counts[match[1]] || strconv.Itoa(n) != match[2] {
			return invalid("prompt references a nonexistent or invalid asset index")
		}
		seen[match[1]][n] = true
	}
	for category, count := range counts {
		for n := 1; n <= count; n++ {
			if !seen[category][n] {
				return invalid(fmt.Sprintf("prompt must reference @%s%d", category, n))
			}
		}
	}
	return r, caps, nil
}

func longXiaReference(category, source string) (longXiaAsset, error) {
	a := longXiaAsset{Category: category}
	if strings.HasPrefix(source, "data:") {
		header, data, ok := strings.Cut(source, ",")
		if !ok || !strings.HasSuffix(header, ";base64") || data == "" || len(data) > 40<<20 {
			return a, fmt.Errorf("LongXia: invalid or oversized reference data URL")
		}
		mime := strings.TrimSuffix(strings.TrimPrefix(header, "data:"), ";base64")
		valid := category == "image" && (mime == "image/png" || mime == "image/jpeg" || mime == "image/webp") || category == "audio" && mime == "audio/mpeg" || category == "video" && mime == "video/mp4"
		if !valid {
			return a, fmt.Errorf("LongXia: unsupported reference media type (audio must be MP3)")
		}
		decoded, err := base64.StdEncoding.Strict().DecodeString(data)
		if err != nil || len(decoded) == 0 {
			return a, fmt.Errorf("LongXia: invalid reference Base64")
		}
		if category == "image" && len(decoded) > 25<<20 || category == "audio" && len(decoded) > 15<<20 {
			return a, fmt.Errorf("LongXia: reference exceeds the media size limit")
		}
		a.Base64 = data
		return a, nil
	}
	u, err := url.Parse(source)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return a, fmt.Errorf("LongXia: reference URL must be a public HTTPS URL")
	}
	if err := rejectPrivateImageHost(source); err != nil {
		return a, fmt.Errorf("LongXia: reference URL must use a public host")
	}
	a.URL = source
	return a, nil
}

func longXiaError(c *gin.Context, status int, code, message string) (*OpenAIForwardResult, error) {
	MarkResponseCommitted(c)
	c.JSON(status, gin.H{"error": gin.H{"type": code, "code": code, "message": message}})
	return nil, fmt.Errorf("LongXia: %s", message)
}

func (s *OpenAIGatewayService) forwardLongXiaVideo(ctx context.Context, c *gin.Context, account *Account, endpoint GrokMediaEndpoint, taskID string, body []byte) (*OpenAIForwardResult, error) {
	if !endpoint.IsSeedance() {
		return longXiaError(c, 400, "invalid_request_error", "LongXia requires the Seedance task endpoint")
	}
	if account.getExtraBool("vividai_enabled") {
		return longXiaError(c, 400, "invalid_request_error", "Choose only one upstream media protocol per account")
	}
	if endpoint == SeedanceEndpointDelete {
		return longXiaError(c, 405, "unsupported_operation", "LongXia does not expose task cancellation")
	}
	base, err := s.validateUpstreamBaseURL(account.GetCredential("base_url"))
	if err != nil {
		return longXiaError(c, 400, "invalid_request_error", "Invalid LongXia base URL")
	}
	base = strings.TrimRight(base, "/")
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	target, method := base+"/videos", http.MethodGet
	result := &OpenAIForwardResult{ResponseID: taskID, UpstreamEndpoint: "/v1/videos"}
	if endpoint == SeedanceEndpointCreate {
		info, parseErr := ParseSeedanceRequest(body)
		if parseErr != nil {
			return longXiaError(c, 400, "invalid_request_error", parseErr.Error())
		}
		payload, caps, parseErr := parseLongXiaRequest(body, account.GetMappedModel(info.Model))
		if parseErr != nil {
			return longXiaError(c, 400, "invalid_request_error", parseErr.Error())
		}
		value, _ := c.Get("api_key")
		key, _ := value.(*APIKey)
		if !s.hasLongXiaPricing(ctx, info.Model, key) {
			return longXiaError(c, 400, "longxia_pricing_required", "Configure per-request or per-second video model pricing before submitting LongXia tasks")
		}
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		method = http.MethodPost
		result.Model, result.BillingModel, result.UpstreamModel = info.Model, info.Model, payload.Model
		result.VideoDurationSeconds, result.VideoResolution = payload.Duration, caps.resolution
	} else {
		if !IsLongXiaTask(taskID) {
			return longXiaError(c, 404, "not_found_error", "LongXia task not found")
		}
		rawID := strings.TrimPrefix(taskID, "seedance:"+longXiaTaskPrefix)
		if rawID == "" || validateUpstreamPathSegment("LongXia task ID", rawID) != nil {
			return longXiaError(c, 400, "invalid_request_error", "Invalid LongXia task ID")
		}
		target += "/" + rawID
		result.UpstreamEndpoint = "/v1/videos/" + rawID
	}
	key := strings.TrimSpace(account.GetCredential("api_key"))
	if key == "" {
		return longXiaError(c, 400, "invalid_request_error", "LongXia account requires an API key")
	}
	SetActualOpenAIUpstreamEndpoint(c, result.UpstreamEndpoint)
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	account.ApplyHeaderOverrides(req.Header)
	proxy := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxy = account.Proxy.URL()
	}
	started := time.Now()
	resp, err := s.httpUpstream.Do(req, proxy, account.ID, account.Concurrency)
	SetOpsLatencyMs(c, OpsUpstreamLatencyMsKey, time.Since(started).Milliseconds())
	// A timeout or 5xx can follow task acceptance. No automatic resubmission.
	if err != nil {
		return longXiaError(c, 502, "upstream_error", "LongXia request interrupted; task acceptance is unknown, do not automatically resubmit")
	}
	defer resp.Body.Close()
	raw, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		// Authentication/limit rejections with no receipt can use the existing
		// scheduler failover. Parameter errors and ambiguous 5xx remain terminal.
		if endpoint == SeedanceEndpointCreate && (resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 429) && gjson.ValidBytes(raw) && gjson.GetBytes(raw, "error").Exists() && !gjson.GetBytes(raw, "id").Exists() && !gjson.GetBytes(raw, "task_id").Exists() {
			s.handleFailoverSideEffects(ctx, resp, account, raw)
			return nil, &UpstreamFailoverError{StatusCode: resp.StatusCode, ResponseBody: raw}
		}
		writeGrokMediaResponse(c, resp, raw, s.responseHeaderFilter)
		return nil, fmt.Errorf("LongXia upstream HTTP %d", resp.StatusCode)
	}
	response, err := normalizeLongXiaResponse(raw, endpoint, taskID, result)
	if err != nil {
		return longXiaError(c, 502, "upstream_error", err.Error())
	}
	result.Duration, result.ResponseHeaders = time.Since(started), resp.Header.Clone()
	MarkResponseCommitted(c)
	c.Header("Retry-After", "30")
	c.JSON(resp.StatusCode, response)
	return result, nil
}

func normalizeLongXiaResponse(raw []byte, endpoint GrokMediaEndpoint, taskID string, result *OpenAIForwardResult) (gin.H, error) {
	if !gjson.ValidBytes(raw) || !gjson.ParseBytes(raw).IsObject() {
		return nil, fmt.Errorf("LongXia returned invalid JSON")
	}
	id, alias := gjson.GetBytes(raw, "id"), gjson.GetBytes(raw, "task_id")
	if id.Exists() && id.Type != gjson.String || alias.Exists() && alias.Type != gjson.String || id.String() != "" && alias.String() != "" && id.String() != alias.String() {
		return nil, fmt.Errorf("LongXia returned inconsistent task IDs")
	}
	rawID := id.String()
	if rawID == "" {
		rawID = alias.String()
	}
	if rawID == "" || validateUpstreamPathSegment("LongXia task ID", rawID) != nil {
		return nil, fmt.Errorf("LongXia returned an invalid task ID")
	}
	publicID := longXiaTaskPrefix + rawID
	result.ResponseID = SeedanceTaskKey(publicID)
	if endpoint == SeedanceEndpointStatus && result.ResponseID != taskID {
		return nil, fmt.Errorf("LongXia returned a different task ID")
	}
	status := gjson.GetBytes(raw, "status").String()
	states := map[string]string{"queued": "queued", "in_progress": "running", "completed": "succeeded", "failed": "failed", "cancelled": "cancelled"}
	normalized, ok := states[status]
	if !ok {
		return nil, fmt.Errorf("LongXia returned an unknown task status")
	}
	response := gin.H{"id": publicID, "status": normalized}
	for _, field := range []string{"created_at", "progress"} {
		if v := gjson.GetBytes(raw, field); v.Exists() {
			response[field] = v.Value()
		}
	}
	if status == "completed" {
		data := gjson.GetBytes(raw, "data")
		if !data.IsArray() || len(data.Array()) != 1 {
			return nil, fmt.Errorf("LongXia completed task must contain one video")
		}
		media := data.Array()[0]
		rawURL := media.Get("url").String()
		u, err := url.Parse(rawURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || rejectPrivateImageHost(rawURL) != nil {
			return nil, fmt.Errorf("LongXia completed task has an invalid result URL")
		}
		if mime := media.Get("media_type").String(); mime != "" && !strings.HasPrefix(mime, "video/") {
			return nil, fmt.Errorf("LongXia returned non-video media for a video task")
		}
		response["content"] = gin.H{"video_url": rawURL}
		for _, field := range []string{"width", "height"} {
			if v := media.Get(field); v.Exists() {
				response[field] = v.Value()
			}
		}
		// Even an immediately completed create is billed on its first status poll,
		// after ownership and the create-time price units have been persisted.
		if endpoint == SeedanceEndpointStatus {
			result.VideoCount = 1
		}
	}
	if status == "failed" || status == "cancelled" {
		upstreamErr := gjson.GetBytes(raw, "error")
		if upstreamErr.Exists() {
			response["error"] = upstreamErr.Value()
		} else {
			response["error"] = gin.H{"code": status, "message": "LongXia task " + status}
		}
	}
	return response, nil
}
