package service

import (
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
)

func isOpenAIModelUnavailableResponse(status int, body []byte) bool {
	return (status == http.StatusBadRequest || status == http.StatusNotFound) &&
		isOpenAIModelUnavailablePayload(body, 0)
}

// Inspect only error envelopes, including gateways that serialize the original
// response into detail/message. Never inspect echoed prompts or request fields.
func isOpenAIModelUnavailablePayload(body []byte, depth int) bool {
	if depth > 4 {
		return false
	}
	if !gjson.ValidBytes(body) {
		return isOpenAIModelUnavailableMessage(string(body))
	}
	fields := gjson.ParseBytes(body)
	if fields.Type == gjson.String {
		message := fields.String()
		if !gjson.Valid(message) {
			return isOpenAIModelUnavailableMessage(message)
		}
		return isOpenAIModelUnavailablePayload([]byte(message), depth+1)
	}
	if !fields.IsObject() {
		return false
	}
	if nested := fields.Get("error"); nested.Exists() {
		return isOpenAIModelUnavailablePayload([]byte(nested.Raw), depth+1)
	}
	code := strings.ToLower(strings.TrimSpace(fields.Get("code").String()))
	errType := strings.ToLower(strings.TrimSpace(fields.Get("type").String()))
	param := strings.TrimSpace(fields.Get("param").String())
	if param != "" && param != "model" {
		return false
	}
	if strings.Contains(errType, "policy") || strings.Contains(errType, "unsafe") || strings.Contains(errType, "moderation") {
		return false
	}
	if code == "model_not_found" {
		return true
	}
	if code != "" {
		// ERR-<10 hex digits> is an opaque gateway error identifier, not a
		// semantic parameter error. Inspect its message for model availability
		// while keeping explicit error codes and types authoritative.
		if !isOpenAIOpaqueGatewayErrorCode(code) {
			return false
		}
		switch errType {
		case "", "invalid_request_error", "not_found_error", "model_not_found":
		default:
			return false
		}
	}
	if errType == "model_not_found" {
		return true
	}
	for _, key := range []string{"message", "detail"} {
		value := fields.Get(key)
		if value.Exists() && isOpenAIModelUnavailablePayload([]byte(value.Raw), depth+1) {
			return true
		}
	}
	return false
}

func isOpenAIOpaqueGatewayErrorCode(code string) bool {
	if len(code) != len("err-")+10 || !strings.HasPrefix(code, "err-") {
		return false
	}
	for _, c := range code[len("err-"):] {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func isOpenAIModelUnavailableMessage(message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	if model, ok := strings.CutPrefix(msg, "unsupported 10k image model:"); ok {
		return strings.TrimSpace(model) != ""
	}
	return strings.Contains(msg, "unknown provider for model") ||
		strings.Contains(msg, "unknown model") ||
		strings.Contains(msg, "model not found") ||
		strings.Contains(msg, "model is not supported") ||
		(strings.HasPrefix(msg, "model ") && strings.Contains(msg, " is not supported by any configured account"))
}
