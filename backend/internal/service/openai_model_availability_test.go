package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIModelUnavailableErrorEnvelopes(t *testing.T) {
	svc := &OpenAIGatewayService{accountRepo: &modelNotFoundManagedAccountRepo{}}
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	for _, body := range []string{
		`{"error":{"message":"Model \"gpt-image-2.5-flare\" is not supported by any configured account in this group","type":"model_not_found"}}`,
		`{"detail":"{\"error\":{\"message\":\"Model \\\"gpt-image-2.5-flare\\\" is not supported by any configured account in this group\",\"type\":\"model_not_found\"}}"}`,
		`{"detail":{"error":{"type":"model_not_found","message":"No such deployment"}}}`,
		`{"error":{"code":"model_not_found","type":"invalid_request_error","param":"model","message":"No such deployment"}}`,
		`{"error":{"message":"Model \"gpt-image-2.5-flare\" is not supported by any configured account in this group"}}`,
		`{"error":{"type":"not_found_error","message":"Model not found: gpt-image-2.5-flare"}}`,
	} {
		for _, status := range []int{http.StatusBadRequest, http.StatusNotFound} {
			require.True(t, svc.shouldFailoverOpenAIUpstreamResponse(account, status, "", []byte(body)), "%d %s", status, body)
		}
	}
	for _, body := range []string{
		`{"error":{"message":"Endpoint not found"}}`,
		`{"error":{"code":"invalid_parameter","message":"model not found"}}`,
		`{"error":{"type":"model_not_found","param":"size"}}`,
		`{"error":{"type":"content_policy_error","code":"model_not_found"}}`,
		`{"detail":"{\"error\":{\"code\":\"content_policy_violation\",\"message\":\"model not found\"}}"}`,
		`{"error":{"message":"invalid request"},"prompt":"model not found","request":{"type":"model_not_found"}}`,
	} {
		require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(account, http.StatusBadRequest, "", []byte(body)), body)
	}
}

func TestOpenAIModelUnavailableAlwaysUsesNextAccount(t *testing.T) {
	for _, status := range []int{400, 404} {
		err := newOpenAIUpstreamFailoverError(status, nil, []byte(`{"error":{"type":"model_not_found"}}`), "", true)
		require.False(t, err.RetryableOnSameAccount)
		require.True(t, err.ShouldRetryNextAccount())
	}
}

func TestOpenAIImagesModelUnavailableSSE(t *testing.T) {
	err := openAIImagesUpstreamErrorFromSSEPayload([]byte(`{"type":"error","error":{"type":"model_not_found","message":"No model in this upstream group"}}`))
	// The stream response has not been committed yet; another account can serve it.
	require.NotNil(t, err)
	require.True(t, IsOpenAIImagesRetryableUpstreamError(err))
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", nil)
	before := c.Writer.Size()
	svc := &OpenAIGatewayService{}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	resp := &http.Response{StatusCode: http.StatusOK, Header: http.Header{}}
	got := svc.handleOpenAIImagesOAuthResponseError(context.Background(), c, account, "gpt-image-2.5-flare", "", resp, before, err)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, got, &failover)
	require.False(t, failover.RetryableOnSameAccount)
	require.False(t, c.Writer.Written())
	_, _ = c.Writer.Write([]byte("already sent image output"))
	got = svc.handleOpenAIImagesOAuthResponseError(context.Background(), c, account, "gpt-image-2.5-flare", "", resp, before, err)
	require.NotErrorAs(t, got, &failover, "do not retry after sending image output")
}
