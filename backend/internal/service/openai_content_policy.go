package service

import (
	"bytes"
	"encoding/json"
	"strings"
)

func openAIContentPolicyCode(body []byte) string { return openAIContentPolicyCodeDepth(body, 0) }

func openAIContentPolicyCodeDepth(body []byte, depth int) string {
	if depth > 8 {
		return ""
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return ""
	}
	if body[0] == '"' {
		var message string
		if json.Unmarshal(body, &message) == nil {
			return openAIContentPolicyTextCode(message, depth)
		}
		return ""
	}
	if body[0] == '[' {
		var items []json.RawMessage
		if json.Unmarshal(body, &items) == nil {
			for _, item := range items {
				if code := openAIContentPolicyCodeDepth(item, depth+1); code != "" {
					return code
				}
			}
		}
		return ""
	}
	if body[0] != '{' {
		return openAIContentPolicyTextCode(string(body), depth)
	}
	// Decode only error fields. Successful image bodies can contain large
	// base64 payloads, which should not be copied into a generic object tree.
	var envelope struct {
		Code              json.RawMessage `json:"code"`
		ErrorCode         json.RawMessage `json:"error_code"`
		Type              json.RawMessage `json:"type"`
		Reason            json.RawMessage `json:"reason"`
		Error             json.RawMessage `json:"error"`
		Errors            json.RawMessage `json:"errors"`
		Detail            json.RawMessage `json:"detail"`
		Response          json.RawMessage `json:"response"`
		Cause             json.RawMessage `json:"cause"`
		InnerError        json.RawMessage `json:"innererror"`
		IncompleteDetails json.RawMessage `json:"incomplete_details"`
		Message           json.RawMessage `json:"message"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return ""
	}
	for _, raw := range []json.RawMessage{envelope.Code, envelope.ErrorCode, envelope.Type, envelope.Reason} {
		var marker string
		if json.Unmarshal(raw, &marker) == nil {
			if code := openAIContentPolicyMarkerCode(marker); code != "" {
				return code
			}
		}
	}
	// Recurse only through known error wrappers, never arbitrary payloads.
	for _, raw := range []json.RawMessage{envelope.Error, envelope.Errors, envelope.Detail, envelope.Response, envelope.Cause, envelope.InnerError, envelope.IncompleteDetails, envelope.Message} {
		if code := openAIContentPolicyCodeDepth(raw, depth+1); code != "" {
			return code
		}
	}
	return ""
}

func openAIContentPolicyMarkerCode(marker string) string {
	marker = strings.ToLower(strings.TrimSpace(marker))
	switch marker {
	case "prompt_unsafe", "image_unsafe", "video_unsafe", "moderation_blocked", "content_filter", "safety_violation":
		return marker
	case "content_policy", "content_policy_violation", "content_policy_error",
		"content_filter_error":
		return "content_policy_violation"
	}
	return ""
}

func openAIContentPolicyTextCode(message string, depth int) string {
	if depth > 8 {
		return ""
	}
	// Decode a gateway's embedded JSON, including bodies followed by a request
	// ID or other suffix. Do not scan that JSON's echoed prompt as prose.
	if start := strings.IndexByte(message, '{'); start >= 0 {
		var wrapped json.RawMessage
		if json.NewDecoder(strings.NewReader(message[start:])).Decode(&wrapped) == nil {
			if code := openAIContentPolicyCodeDepth(wrapped, depth+1); code != "" {
				return code
			}
			message = message[:start]
		}
	}
	lower := strings.ToLower(message)
	if strings.Contains(lower, "generated images appear to be unsafe") {
		return "image_unsafe"
	}
	for _, phrase := range []string{
		"content_policy_violation", "content_policy_error", "moderation_blocked",
		"violates our content policy", "violate our content policy", "violated our content policy",
		"violates the content policy", "blocked by our content policy", "rejected by the content safety filter",
		"rejected by the safety system", "blocked by the safety system", "did not pass moderation",
		"违反了我们的内容政策", "违反内容政策", "违反了内容政策", "违反安全政策",
		"未通过内容安全审核", "未通过内容审核", "涉及违规内容", "违反了关于与第三方内容相似性的防护限制",
	} {
		if strings.Contains(lower, phrase) {
			return "content_policy_violation"
		}
	}
	return ""
}
