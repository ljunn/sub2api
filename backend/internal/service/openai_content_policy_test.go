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
