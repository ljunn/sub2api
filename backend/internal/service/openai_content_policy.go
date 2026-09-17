package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// Only inspect error fields. A prompt echoed elsewhere in a response must not
// turn an infrastructure failure into a content refusal.
func openAIContentPolicyCode(body []byte) string {
	return openAIContentPolicyCodeDepth(body, 0)
}

func openAIContentPolicyCodeDepth(body []byte, depth int) string {
	for _, path := range []string{"error.code", "error.type", "error.error_code", "error_code"} {
		marker := strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, path).String()))
		switch marker {
		case "image_unsafe", "prompt_unsafe", "video_unsafe", "moderation_blocked", "content_filter", "safety_violation", "content_policy_violation":
			return marker
		case "content_policy_error", "content_filter_error":
			return "content_policy_violation"
		}
	}
	message := gjson.GetBytes(body, "error.message").String()
	if message == "" {
		message = gjson.GetBytes(body, "message").String()
	}
	// Some gateways wrap the original JSON in "poll failed: 451 {...}".
	if depth < 2 {
		if start := strings.IndexByte(message, '{'); start >= 0 && gjson.Valid(message[start:]) {
			if code := openAIContentPolicyCodeDepth([]byte(message[start:]), depth+1); code != "" {
				return code
			}
		}
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "generated images appear to be unsafe") {
		return "image_unsafe"
	}
	return ""
}
