//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type modelUnavailableImagesUpstream struct {
	service.HTTPUpstream
	status         int
	body           string
	calls          []int64
	secondSucceeds bool
}

func (u *modelUnavailableImagesUpstream) Do(_ *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	u.calls = append(u.calls, id)
	status, body := u.status, u.body
	if id == 2 {
		status, body = http.StatusServiceUnavailable, `{"error":{"type":"api_error","message":"temporarily unavailable"}}`
		if u.secondSucceeds {
			status, body = http.StatusOK, `{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`
		}
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewBufferString(body))}, nil
}

type modelUnavailableImagesUsageRepo struct {
	service.UsageLogRepository
	logs []*service.UsageLog
}

func (r *modelUnavailableImagesUsageRepo) Create(_ context.Context, entry *service.UsageLog) (bool, error) {
	r.logs = append(r.logs, entry)
	return true, nil
}

func TestOpenAIGatewayHandlerImages_ModelUnavailableSwitchesAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name                string
		status              int
		body                string
		secondSupportsModel bool
		wantCalls           []int64
		wantStatus          int
	}{
		{"opaque 10k model rejection switches and succeeds", 400, `{"error":{"code":"ERR-88984A453A","message":"unsupported 10k image model: gpt-image-medium","param":"","type":"invalid_request_error"}}`, true, []int64{1, 2}, 200},
		{"opaque 10k model rejection exhausts available channels", 400, `{"error":{"code":"ERR-0983D5FFBA","message":"unsupported 10k image model: gpt-image-medium","type":"invalid_request_error"}}`, true, []int64{1, 2}, 502},
		{"opaque 10k model rejection with no compatible backup", 400, `{"error":{"code":"ERR-88984A453A","message":"unsupported 10k image model: gpt-image-medium","type":"invalid_request_error"}}`, false, []int64{1}, 400},
		{"opaque code with size parameter remains terminal", 400, `{"error":{"code":"ERR-88984A453A","param":"size","message":"unsupported 10k image model: gpt-image-medium","type":"invalid_request_error"}}`, true, []int64{1}, 500},
		{"opaque code with content refusal remains terminal", 400, `{"error":{"code":"ERR-88984A453A","type":"content_policy_error","message":"unsupported 10k image model: gpt-image-medium"}}`, true, []int64{1}, 400},
		{"type only 400", 400, `{"error":{"type":"model_not_found","message":"Model \"gpt-image-2.5-flare\" is not supported by any configured account in this group"}}`, true, []int64{1, 2}, 502},
		{"type only 404 without cooldown service", 404, `{"error":{"type":"model_not_found","message":"Model \"gpt-image-2.5-flare\" is not supported by any configured account in this group"}}`, true, []int64{1, 2}, 502},
		{"detail string", 400, `{"detail":"{\"error\":{\"message\":\"No such deployment\",\"type\":\"model_not_found\"}}"}`, true, []int64{1, 2}, 502},
		{"detail object", 404, `{"detail":{"error":{"type":"model_not_found"}}}`, true, []int64{1, 2}, 502},
		{"remaining account does not support model", 404, `{"error":{"type":"model_not_found"}}`, false, []int64{1}, 502},
		{"parameter rejection remains terminal", 400, `{"error":{"code":"invalid_parameter","message":"Invalid size"}}`, true, []int64{1}, 500},
		{"nested 429 refusal remains terminal", 429, `{"error":{"detail":{"code":"image_unsafe"}}}`, true, []int64{1}, 400},
		{"nested 503 refusal remains terminal", 503, `{"detail":"{\"error\":{\"code\":\"content_policy_violation\"}}"}`, true, []int64{1}, 400},
		{"Chinese 403 refusal remains terminal", 403, `{"error":{"message":"该提示可能违反了我们的内容政策，请修改提示语。"}}`, true, []int64{1}, 400},
		{"text 502 refusal remains terminal", 502, `{"error":{"message":"Your request was rejected by the safety system."}}`, true, []int64{1}, 400},
		{"content rejection remains terminal", 400, `{"error":{"code":"content_policy_violation","message":"blocked"}}`, true, []int64{1}, 400},
	} {
		for _, endpoint := range []string{"generations", "edits"} {
			t.Run(tt.name+"/"+endpoint, func(t *testing.T) {
				model := "gpt-image-2"
				accounts := []service.Account{
					{ID: 1, Name: "first", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 0, Credentials: map[string]any{
						"api_key": "test-1", "pool_mode": true, "pool_mode_retry_status_codes": []any{400, 403, 404, 429, 503},
						"custom_error_codes_enabled": true, "custom_error_codes": []any{float64(500)},
						"model_mapping": map[string]any{model: "gpt-image-medium"},
					}},
					{ID: 2, Name: "second", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 1, Credentials: map[string]any{
						"api_key": "test-2", "pool_mode": true, "pool_mode_retry_status_codes": []any{400, 404}, "model_mapping": map[string]any{model: model},
					}},
				}
				if !tt.secondSupportsModel {
					accounts[1].Credentials["model_mapping"] = map[string]any{"gpt-image-2.5-flare": "gpt-image-2.5-flare"}
				}
				upstream := &modelUnavailableImagesUpstream{status: tt.status, body: tt.body, secondSucceeds: tt.wantStatus == http.StatusOK}
				cfg := &config.Config{RunMode: config.RunModeSimple}
				usage := &modelUnavailableImagesUsageRepo{}
				pricing := service.NewBillingService(cfg, nil)
				gateway := service.NewOpenAIGatewayService(openAIImagesFailoverAccountRepo{accounts: accounts}, usage, nil, nil, nil, nil, nil, cfg, nil, nil, pricing, nil, nil, upstream, &service.DeferredService{}, nil, nil, service.NewModelPricingResolver(nil, pricing), nil, nil, nil, nil)
				billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
				t.Cleanup(billing.Stop)
				h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
				h.maxAccountSwitches = 2
				body := []byte(`{"model":"gpt-image-2","prompt":"draw a cat"}`)
				contentType := "application/json"
				if endpoint == "edits" {
					var buf bytes.Buffer
					writer := multipart.NewWriter(&buf)
					require.NoError(t, writer.WriteField("model", model))
					require.NoError(t, writer.WriteField("prompt", "edit the reference"))
					part, err := writer.CreateFormFile("image[]", "reference.png")
					require.NoError(t, err)
					_, err = part.Write([]byte("\x89PNG\r\n\x1a\nreference"))
					require.NoError(t, err)
					require.NoError(t, writer.Close())
					body, contentType = buf.Bytes(), writer.FormDataContentType()
				}
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/"+endpoint, bytes.NewReader(body))
				c.Request.Header.Set("Content-Type", contentType)
				groupID := int64(3130)
				c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 99, GroupID: &groupID, Group: &service.Group{ID: groupID, AllowImageGeneration: true}, User: &service.User{ID: 100}})
				c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100, Concurrency: 0})
				h.Images(c)
				require.Equal(t, tt.wantCalls, upstream.calls, "model availability failure must skip same-account retry and respect model support")
				require.Equal(t, tt.wantStatus, rec.Code)
				if tt.wantStatus == http.StatusOK {
					require.Contains(t, rec.Body.String(), `"b64_json":"aGVsbG8="`)
					require.Len(t, usage.logs, 1, "only the successful attempt is billed")
				}
			})
		}
	}
}
