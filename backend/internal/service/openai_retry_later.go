package service

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Some compatible gateways use HTTP 400 for temporary upstream failures. A
// specific content/parameter code overrides their generic retry-later message.
// Read only explicit error fields so echoed request text cannot cause failover.
func isOpenAIRetryLaterUpstreamError(status int, body []byte) bool {
	if status != http.StatusBadRequest {
		return false
	}
	message := strings.TrimSpace(string(body))
	if json.Valid(body) {
		var response struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    string `json:"code"`
				Param   string `json:"param"`
			} `json:"error"`
		}
		if json.Unmarshal(body, &response) != nil || response.Error.Param != "" {
			return false
		}
		for _, marker := range []string{response.Error.Type, response.Error.Code} {
			switch strings.ToLower(strings.TrimSpace(marker)) {
			case "", "api_error", "upstream_error", "server_error":
			default:
				return false
			}
		}
		message = strings.TrimSpace(response.Error.Message)
	}
	return strings.EqualFold(strings.TrimSuffix(message, "."), "Upstream request failed. Please retry later")
}
