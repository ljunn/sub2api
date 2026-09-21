//go:build unit

package handler

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type retryLaterImagesUpstream struct {
	service.HTTPUpstream
	firstError  string
	firstStatus int
	accountIDs  []int64
}

func (u *retryLaterImagesUpstream) Do(_ *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.accountIDs = append(u.accountIDs, accountID)
	status, body := u.firstStatus, u.firstError
	if status == 0 {
		status = http.StatusBadRequest
	}
	if accountID == 2 {
		status, body = http.StatusServiceUnavailable, `{"error":{"type":"api_error","message":"temporarily unavailable"}}`
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewBufferString(body))}, nil
}

func TestOpenAIGatewayHandlerImages_RetryLater400SwitchesAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name        string
		firstError  string
		wantCalls   []int64
		wantStatus  int
		firstStatus int
	}{
		{"429 with retries disabled switches immediately", `{"error":{"message":"engine temporarily unavailable"}}`, []int64{1, 2}, http.StatusBadGateway, http.StatusTooManyRequests},
		{"generic upstream failure", `{"error":{"type":"api_error","message":"Upstream request failed. Please retry later."}}`, []int64{1, 2}, http.StatusBadGateway, http.StatusBadRequest},
		{"mislabelled transient switches", `{"error":{"type":"invalid_request_error","code":"upstream_error","message":"Upstream request failed. Please retry later."}}`, []int64{1, 2}, http.StatusBadGateway, http.StatusBadRequest},
		{"wrapped unsafe is terminal", `{"error":{"type":"api_error","message":"poll failed: 451 {\"error_code\":\"image_unsafe\",\"message\":\"The generated images appear to be unsafe.\"}"}}`, []int64{1}, http.StatusBadRequest, http.StatusBadRequest},
		{"content refusal is terminal", `{"error":{"type":"upstream_error","code":"content_policy_violation","message":"Upstream request failed. Please retry later."}}`, []int64{1}, http.StatusBadRequest, http.StatusBadRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			accounts := []service.Account{
				{ID: 1, Name: "first", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 0, Credentials: map[string]any{"api_key": "test-1", "pool_mode": true}},
				{ID: 2, Name: "second", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 1, Credentials: map[string]any{"api_key": "test-2", "pool_mode": true}},
			}
			if tt.firstStatus == http.StatusTooManyRequests {
				for i := range accounts {
					accounts[i].Credentials["pool_mode_retry_count"] = 0
				}
			}
			upstream := &retryLaterImagesUpstream{firstError: tt.firstError, firstStatus: tt.firstStatus}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			gateway := service.NewOpenAIGatewayService(
				openAIImagesFailoverAccountRepo{accounts: accounts}, nil, nil, nil, nil, nil, nil,
				cfg, nil, nil, nil, nil, nil, upstream, nil, nil, nil, nil, nil, nil, nil, nil,
			)
			billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billing.Stop)
			h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing,
				service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
			h.maxAccountSwitches = 1
			body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat"}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
			c.Request.Header.Set("Content-Type", "application/json")
			groupID := int64(3130)
			c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 99, GroupID: &groupID,
				Group: &service.Group{ID: groupID, AllowImageGeneration: true}, User: &service.User{ID: 100}})
			c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100, Concurrency: 0})
			h.Images(c)
			require.Equal(t, tt.wantCalls, upstream.accountIDs, "each eligible channel must be tried at most once")
			require.Equal(t, tt.wantStatus, rec.Code)
		})
	}
}
