package service

import (
	"fmt"
	"math"
	"mime"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	SiteVideoFormatH3   = "minimax-h3"
	SiteVideoFormatWan3 = "wan3"
)

// These upstream platform identifiers describe distinct video protocols, not
// local account platforms. Match both platform and model before bridging them.
func siteVideoFormat(kind string, model SiteModel) string {
	if kind != "sub2api" {
		return ""
	}
	switch {
	case model.Platform == "powerby-h3" && model.Model == "minimax-h3":
		return SiteVideoFormatH3
	case model.Platform == "wan3" && (model.Model == "wan3.0-video" || model.Model == "wan3.0-video-prime"):
		return SiteVideoFormatWan3
	}
	return ""
}

func siteVideoPlatformCompatible(kind string, model SiteModel, platform string) bool {
	format := siteVideoFormat(kind, model)
	return format == SiteVideoFormatH3 && platform == PlatformMiniMax || format == SiteVideoFormatWan3 && platform == PlatformOpenAI
}

func (a *Account) SupportsSiteVideoRelay() bool {
	if a == nil || a.Type != AccountTypeAPIKey || strings.TrimSpace(a.GetCredential("base_url")) == "" {
		return false
	}
	format := accountGrokMediaAPIFormat(a)
	return a.Platform == PlatformMiniMax && format == SiteVideoFormatH3 || a.Platform == PlatformOpenAI && format == SiteVideoFormatWan3
}

func validSiteVideoFormat(format string) bool {
	return format == "xai" || format == GrokMediaAPIFormatOpenAI || format == SiteVideoFormatH3 || format == SiteVideoFormatWan3
}

// Parse the documented JSON contracts before scheduling. In particular Wan3
// permits 30 seconds; the xAI-specific 15-second billing clamp must not apply.
func ParseSiteVideoRelayRequest(platform, contentType string, body []byte) (GrokMediaRequestInfo, error) {
	info := ParseGrokMediaRequest(contentType, body)
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil || mediaType != "application/json" || !gjson.ValidBytes(body) || !gjson.ParseBytes(body).IsObject() {
		return info, fmt.Errorf("video request must be a JSON object")
	}
	if info.Model == "" || info.Prompt == "" {
		return info, fmt.Errorf("model and prompt are required")
	}
	minimum, maximum, fallback := 2, 30, 5
	if platform == PlatformMiniMax {
		minimum, maximum, fallback = 4, 15, 15
	}
	duration := gjson.GetBytes(body, "duration")
	seconds := gjson.GetBytes(body, "seconds")
	if !duration.Exists() {
		duration = seconds
	}
	info.DurationSeconds = fallback
	if duration.Exists() {
		if duration.Type != gjson.Number || math.Trunc(duration.Float()) != duration.Float() || duration.Int() < int64(minimum) || duration.Int() > int64(maximum) {
			return info, fmt.Errorf("duration must be an integer between %d and %d", minimum, maximum)
		}
		if seconds.Exists() && (seconds.Type != gjson.Number || seconds.Float() != duration.Float()) {
			return info, fmt.Errorf("duration and seconds must match")
		}
		info.DurationSeconds = int(duration.Int())
	}
	resolution := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "resolution").String()))
	// Billing keeps the upstream group's existing 720p price key. Its native
	// H3 wire tier is 768P; never use this billing alias as a pixel size.
	if resolution == "" || (platform == PlatformMiniMax && resolution == "768p") {
		resolution = "720p"
	}
	if (platform == PlatformMiniMax && resolution != "720p") || (platform != PlatformMiniMax && resolution != "480p" && resolution != "720p" && resolution != "1080p") {
		return info, fmt.Errorf("unsupported video resolution for the configured relay contract")
	}
	info.Resolution = resolution
	info.AspectRatio = firstNonEmpty(gjson.GetBytes(body, "ratio").String(), info.AspectRatio, "16:9")
	for _, path := range []string{"image_urls", "first_frame", "last_frame"} {
		v := gjson.GetBytes(body, path)
		if v.IsArray() {
			for _, image := range v.Array() {
				info.InputImageURLs = append(info.InputImageURLs, image.String())
			}
		} else if v.Type == gjson.String {
			info.InputImageURLs = append(info.InputImageURLs, v.String())
		}
	}
	for _, item := range gjson.GetBytes(body, "media").Array() {
		if kind := item.Get("type").String(); kind == "first_frame" || kind == "reference_image" {
			info.InputImageURLs = append(info.InputImageURLs, item.Get("url").String())
		}
	}
	return info, nil
}

func prepareSiteVideoRelayBody(account *Account, endpoint GrokMediaEndpoint, body []byte, contentType string) ([]byte, string, error) {
	if !endpoint.RequiresRequestBody() {
		return body, contentType, nil
	}
	info, err := ParseSiteVideoRelayRequest(account.Platform, contentType, body)
	if err != nil {
		return nil, "", err
	}
	// Send explicit defaults so billing and the upstream cannot choose different
	// durations or resolution tiers. Preserve native reference fields unchanged.
	wireResolution := strings.ToUpper(info.Resolution)
	if account.Platform == PlatformMiniMax {
		wireResolution = "768P"
	}
	for key, value := range map[string]any{"duration": info.DurationSeconds, "resolution": wireResolution, "ratio": info.AspectRatio} {
		body, err = sjson.SetBytes(body, key, value)
		if err != nil {
			return nil, "", err
		}
	}
	return body, "application/json", nil
}

func NormalizeMediaVideoDuration(model, upstreamModel string, seconds int) int {
	if model == "wan3.0-video" || model == "wan3.0-video-prime" || upstreamModel == "wan3.0-video" || upstreamModel == "wan3.0-video-prime" {
		if seconds <= 0 {
			return 5
		}
		return min(30, max(2, seconds))
	}
	return NormalizeVideoBillingDurationSecondsOrDefault(seconds)
}
