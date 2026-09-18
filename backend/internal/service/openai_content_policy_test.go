package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIImagesContentPolicyNormalization(t *testing.T) {
	for _, tt := range []struct {
		name, body, code string
		status           int
	}{
		{"nested detail", `{"error":{"type":"upstream_error","detail":{"error":{"code":"content_policy_violation"}}}}`, "content_policy_violation", 429},
		{"detail string", `{"detail":"{\"error\":{\"code\":\"image_unsafe\"}}"}`, "image_unsafe", 503},
		{"plain refusal", `{"error":{"message":"Your request was rejected by the safety system."}}`, "content_policy_violation", 403},
		{"Chinese refusal", `{"error":{"message":"该提示可能违反了我们的内容政策，请修改提示语。"}}`, "content_policy_violation", 502},
		{"generic content refusal", `{"error":{"type":"invalid_request_error","code":"content_policy_violation","message":"Upstream request failed. Please retry later."}}`, "content_policy_violation", 400},
		{"wrapped unsafe", `{"error":{"type":"api_error","message":"poll failed: 451 {\"error_code\":\"image_unsafe\",\"message\":\"blocked\"}"}}`, "image_unsafe", 400},
		{"native unsafe", `{"error_code":"prompt_unsafe","message":"blocked"}`, "prompt_unsafe", 451},
		{"opaque code with policy type", `{"error":{"type":"content_policy_error","code":"ERR-962B55B865","message":"The generated images appear to be unsafe."}}`, "content_policy_violation", 451},
		{"wrapped server status", `{"error":{"code":"content_policy_violation","message":"blocked"}}`, "content_policy_violation", 502},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			svc := &OpenAIGatewayService{}
			require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(nil, tt.status, "", body))
			err := openAIImagesUpstreamErrorFromHTTP(tt.status, nil, body)
			require.Equal(t, tt.code, err.Code)
			require.Equal(t, "content_policy_error", err.ErrorType)
			require.False(t, IsOpenAIImagesRetryableUpstreamError(err))
			require.NotContains(t, err.Message, "retry later")
			require.NotContains(t, err.Message, "poll failed")
			if tt.status == 451 {
				require.Equal(t, 451, err.StatusCode)
			} else {
				require.Equal(t, http.StatusBadRequest, err.StatusCode)
			}
		})
	}
	for _, body := range []string{
		`{"error":{"message":"upstream unavailable"},"prompt":"image_unsafe"}`,
		`{"error":{"type":"upstream_error","message":"Upstream request failed. Please retry later."}}`,
		`{"error":{"message":"unsafe parameter value","param":"size"}}`,
	} {
		require.Empty(t, openAIContentPolicyCode([]byte(body)))
	}
}

func TestOpenAIImagesUnsafeSSEIsTerminal(t *testing.T) {
	for _, payload := range []string{
		`{"type":"error","error":{"code":"image_unsafe","message":"blocked"}}`,
		`{"type":"response.failed","response":{"id":"response-id","error":{"type":"content_policy_error","message":"Upstream request failed. Please retry later."}}}`,
	} {
		err := openAIImagesUpstreamErrorFromSSEPayload([]byte(payload))
		require.NotNil(t, err)
		require.False(t, IsOpenAIImagesRetryableUpstreamError(err))
		require.Equal(t, "content_policy_error", err.ErrorType)
		require.NotContains(t, err.Message, "retry later")
	}
}

func TestContentRefusalOverridesRetryableImageStatus(t *testing.T) {
	for _, status := range []int{429, 500, 502, 503} {
		err := &OpenAIImagesUpstreamError{StatusCode: status, Code: "content_policy_violation", ErrorType: "api_error", Message: "blocked"}
		require.False(t, IsOpenAIImagesRetryableUpstreamError(err))
	}
	for _, body := range []string{
		`{"detail":{"prompt":"image_unsafe","message":"unavailable"}}`,
		`{"error":{"message":"poll failed: {\"prompt\":\"content_policy_violation\",\"error\":{\"message\":\"unavailable\"}} request-id=123"}}`,
		`{"response":{"output":[{"type":"message","content":[{"text":"content_policy_violation"}]}]}}`,
	} {
		require.Empty(t, openAIContentPolicyCode([]byte(body)))
	}
}
