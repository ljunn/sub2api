package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestForwardImagesRetryLater400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name     string
		body     string
		failover bool
	}{
		{"api error", `{"error":{"message":"Upstream request failed. Please retry later.","type":"api_error"}}`, true},
		{"upstream error", `{"error":{"message":"Upstream request failed. Please retry later.","type":"upstream_error"}}`, true},
		{"generic server code", `{"error":{"message":"Upstream request failed. Please retry later.","type":"server_error","code":"server_error"}}`, true},
		{"plain text", "Upstream request failed. Please retry later.", true},
		{"case and whitespace", `{"error":{"message":"  UPSTREAM REQUEST FAILED. PLEASE RETRY LATER  "}}`, true},
		{"content refusal", `{"error":{"message":"Upstream request failed. Please retry later.","type":"upstream_error","code":"content_policy_violation"}}`, false},
		{"invalid request", `{"error":{"message":"Upstream request failed. Please retry later.","type":"invalid_request_error"}}`, false},
		{"specific parameter", `{"error":{"message":"Upstream request failed. Please retry later.","type":"api_error","param":"size"}}`, false},
		{"validation code", `{"error":{"message":"Upstream request failed. Please retry later.","type":"api_error","code":"validation_error"}}`, false},
		{"echoed prompt", `{"error":{"message":"Invalid prompt","type":"api_error"},"prompt":"Upstream request failed. Please retry later."}`, false},
		{"ordinary 400", `{"error":{"message":"Invalid size"}}`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat"}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewBufferString(tt.body))}}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			parsed, err := svc.ParseOpenAIImagesRequest(c, body)
			require.NoError(t, err)
			account := &Account{ID: 51, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test-key"}}
			_, err = svc.ForwardImages(context.Background(), c, account, body, parsed, "")
			var failoverErr *UpstreamFailoverError
			if tt.failover {
				require.ErrorAs(t, err, &failoverErr)
				require.Equal(t, http.StatusBadRequest, failoverErr.StatusCode)
				require.False(t, failoverErr.RetryableOnSameAccount)
				require.False(t, c.Writer.Written(), "failover must happen before returning the 400")
			} else {
				require.NotErrorAs(t, err, &failoverErr)
				require.True(t, c.Writer.Written())
				require.Equal(t, http.StatusBadRequest, rec.Code)
			}
		})
	}
}
