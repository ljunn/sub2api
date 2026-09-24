package service

import (
	"encoding/json"
	"fmt"
	"mime"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const GrokMediaAPIFormatOpenAI = "openai"
const GrokMediaAPIFormatCredentialKey = "grok_media_api_format"
const siteVideoAPIFormatCredentialKey = "upstream_site_video_api_format"

// Account platform controls downstream routing; this setting only selects the
// upstream media contract. Managed sites advertise the contract per model.
func accountGrokMediaAPIFormat(account *Account) string {
	if account == nil || account.Type != AccountTypeAPIKey {
		return "xai"
	}
	if format := account.GetCredential(GrokMediaAPIFormatCredentialKey); validSiteVideoFormat(format) {
		return format
	}
	if policy, managed := account.SitePolicy(); managed && policy.BindingID != "" {
		if format := policy.VideoAPIFormat; validSiteVideoFormat(format) {
			return format
		}
		if format := account.GetCredential(siteVideoAPIFormatCredentialKey); validSiteVideoFormat(format) {
			return format
		}
	}
	return "xai"
}

// Older instances sharing the database rewrite site documents and policies
// without unknown fields. Keep the last advertised format in the untyped
// credential map too, independently of the user's explicit format override.
func cacheSiteGrokMediaAPIFormat(account *Account, policy SiteAccountPolicy) bool {
	if (account.Platform != PlatformGrok && account.Platform != PlatformGemini && account.Platform != PlatformMiniMax && account.Platform != PlatformOpenAI) || account.Type != AccountTypeAPIKey || policy.BindingID == "" || policy.BindingID != account.GetCredential(SiteBindingCredentialKey) {
		return false
	}
	format := policy.VideoAPIFormat
	if !validSiteVideoFormat(format) || account.GetCredential(siteVideoAPIFormatCredentialKey) == format {
		return false
	}
	account.Credentials[siteVideoAPIFormatCredentialKey] = format
	return true
}

func prepareAccountGrokMediaBody(account *Account, endpoint GrokMediaEndpoint, body []byte, contentType string) ([]byte, string, error) {
	if account.SupportsSiteVideoRelay() {
		return prepareSiteVideoRelayBody(account, endpoint, body, contentType)
	}
	if endpoint != GrokMediaEndpointVideosGenerations || accountGrokMediaAPIFormat(account) != GrokMediaAPIFormatOpenAI {
		return body, contentType, nil
	}
	info := ParseGrokMediaRequest(contentType, body)
	payload := map[string]any{}
	if gjson.ValidBytes(body) {
		if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
			return nil, "", fmt.Errorf("video request must be a JSON object")
		}
	} else {
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil || mediaType != "multipart/form-data" {
			return nil, "", fmt.Errorf("video request must be JSON or multipart form data")
		}
		payload["model"], payload["prompt"] = info.Model, info.Prompt
		if info.AspectRatio != "" {
			payload["aspect_ratio"] = info.AspectRatio
		}
		refs := append([]string(nil), info.InputImageURLs...)
		for _, upload := range info.Uploads {
			ref, err := openAIImageUploadToDataURL(upload)
			if err != nil {
				return nil, "", err
			}
			refs = append(refs, ref)
		}
		if len(refs) == 1 {
			payload["input_reference"] = refs[0]
		} else if len(refs) > 1 {
			payload["reference_images"] = refs
		}
	}
	// OpenAI Videos uses seconds, while downstream Grok clients use duration.
	// Make resolution explicit: some relays bill their most expensive tier when omitted.
	payload["seconds"] = strconv.Itoa(info.DurationSeconds)
	if _, exists := payload["resolution"]; !exists {
		payload["resolution"] = info.Resolution
	}
	if _, exists := payload["input_reference"]; !exists {
		if ref := extractGrokMediaImageURL(gjson.GetBytes(body, "image")); ref != "" {
			payload["input_reference"] = ref
		}
	}
	if _, exists := payload["reference_images"]; !exists {
		if refs, exists := payload["images"]; exists {
			payload["reference_images"] = refs
		}
	}
	delete(payload, "duration")
	delete(payload, "image")
	delete(payload, "images")
	out, err := json.Marshal(payload)
	return out, "application/json", err
}

// Keep the relay's outer task ID (needed for polling) and expose xAI's response
// fields to downstream Grok clients. Some New API relays nest results twice.
func normalizeAccountGrokVideoResponse(account *Account, endpoint GrokMediaEndpoint, body []byte) []byte {
	if (accountGrokMediaAPIFormat(account) != GrokMediaAPIFormatOpenAI && !account.SupportsSiteVideoRelay()) || (endpoint != GrokMediaEndpointVideosGenerations && endpoint != GrokMediaEndpointVideoStatus) || !gjson.ValidBytes(body) {
		return body
	}
	out := body
	set := func(path string, value any) {
		if next, err := sjson.SetBytes(out, path, value); err == nil {
			out = next
		}
	}
	first := func(paths ...string) string {
		for _, path := range paths {
			if value := strings.TrimSpace(gjson.GetBytes(body, path).String()); value != "" {
				return value
			}
		}
		return ""
	}
	if id := first("id", "task_id", "data.task_id", "request_id"); id != "" {
		set("request_id", id)
	}
	status := strings.ToLower(first("status", "data.status", "data.data.status"))
	switch status {
	case "completed", "success", "succeeded":
		status = "done"
	case "queued", "processing", "in_progress", "submitted", "running":
		status = "pending"
	case "failure", "error":
		status = "failed"
	}
	if status != "" {
		set("status", status)
	}
	if value := first("video.url", "video_url", "url", "download_url", "output.0.url", "result.video_url", "result.url", "result_url", "data.data.video.url", "data.data.video_url", "data.data.url", "data.data.result_url", "data.video_url", "data.url"); value != "" {
		set("video.url", value)
	}
	if seconds, err := strconv.Atoi(strings.TrimSuffix(first("video.duration", "seconds", "duration", "data.data.video.duration", "data.data.duration", "data.data.seconds"), "s")); err == nil && seconds > 0 {
		set("video.duration", seconds)
	}
	return out
}
