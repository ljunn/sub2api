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
		return isOpenAIModelUnavailablePayload([]byte(fields.String()), depth+1)
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
	if code != "" {
		return code == "model_not_found"
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

func isOpenAIModelUnavailableMessage(message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(msg, "unknown provider for model") ||
		strings.Contains(msg, "model not found") ||
		strings.Contains(msg, "model is not supported") ||
		(strings.HasPrefix(msg, "model ") && strings.Contains(msg, " is not supported by any configured account"))
}
