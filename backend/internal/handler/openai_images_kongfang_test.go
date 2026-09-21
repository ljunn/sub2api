//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type kongfangEditsFailoverUpstream struct {
	service.HTTPUpstream
	ids    []int64
	paths  []string
	bodies [][]byte
}

func (u *kongfangEditsFailoverUpstream) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	u.ids = append(u.ids, id)
	u.paths = append(u.paths, req.URL.Path)
	body, _ := io.ReadAll(req.Body)
	u.bodies = append(u.bodies, body)
	status, response := http.StatusInternalServerError, `{"error":{"message":"Upstream gateway error"}}`
	if id == 3 {
		status, response = http.StatusOK, `{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}, "X-Request-Id": []string{"kongfang-edit"}}, Body: io.NopCloser(bytes.NewBufferString(response))}, nil
}

type kongfangEditsUsageRepo struct {
	service.UsageLogRepository
	logs []*service.UsageLog
}

func (r *kongfangEditsUsageRepo) Create(_ context.Context, entry *service.UsageLog) (bool, error) {
	r.logs = append(r.logs, entry)
	return true, nil
}

func TestOpenAIGatewayHandlerImages_KongfangMultipartTriesLowerPrioritiesAndKeepsPrice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	model := "gpt-image-2.5-sunburst"
	p := service.SiteAccountPolicy{SiteKind: "kongfang", BindingID: "binding", Image: true, LocalModel: model, Enabled: true, FreshUntil: time.Now().Add(time.Minute)}
	for i, tier := range []string{"1K", "2K", "4K"} {
		p.Tiers = append(p.Tiers, service.SitePriceTier{Key: tier, Unit: "USD/image", Prices: map[string]float64{"request": []float64{.025, .0275, .0325}[i]}})
		p.Limits = append(p.Limits, service.SiteTierLimit{Key: tier, Unit: "USD/image", Enabled: true, Limits: map[string]float64{"request": .05}})
	}
	rawPolicy, err := json.Marshal(p)
	require.NoError(t, err)
	var policy map[string]any
	require.NoError(t, json.Unmarshal(rawPolicy, &policy))
	accounts := []service.Account{
		{ID: 1, Name: "failing primary", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 0, Credentials: map[string]any{"api_key": "primary"}},
		{ID: 2, Name: "kongfang backup", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: 1,
			Credentials: map[string]any{"api_key": "backup", service.SiteBindingCredentialKey: "binding"}, Extra: map[string]any{service.SitePolicyExtraKey: policy}},
	}
	third := accounts[1]
	third.ID, third.Priority, third.Name = 3, 2, "lower priority kongfang"
	fourth := third
	fourth.ID, fourth.Priority, fourth.Name = 4, 3, "unused last priority"
	accounts = append(accounts, third, fourth)
	upstream := &kongfangEditsFailoverUpstream{}
	usage := &kongfangEditsUsageRepo{}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	billing := service.NewBillingService(cfg, nil)
	gateway := service.NewOpenAIGatewayService(
		openAIImagesFailoverAccountRepo{accounts: accounts}, usage, nil, nil, nil, nil, nil,
		cfg, nil, nil, billing, nil, nil, upstream, &service.DeferredService{}, nil, nil, service.NewModelPricingResolver(nil, billing), nil, nil, nil, nil,
	)
	cache := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(cache.Stop)
	h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), cache,
		service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	h.maxAccountSwitches = 3
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	for _, field := range [][2]string{{"model", model}, {"prompt", "preserve reference"}, {"size", "5464x3072"}} {
		require.NoError(t, w.WriteField(field[0], field[1]))
	}
	part, err := w.CreateFormFile("image", "reference.png")
	require.NoError(t, err)
	_, err = part.Write([]byte("\x89PNG\r\n\x1a\nreference"))
	require.NoError(t, err)
	require.NoError(t, w.Close())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", bytes.NewReader(body.Bytes()))
	core, observed := observer.New(zap.DebugLevel)
	c.Request = c.Request.WithContext(logger.IntoContext(context.Background(), zap.New(core)))
	c.Request.Header.Set("Content-Type", w.FormDataContentType())
	groupID, price := int64(3130), .05
	c.Set(string(middleware2.ContextKeyAPIKey), &service.APIKey{ID: 99, GroupID: &groupID,
		Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, AllowImageGeneration: true, RateMultiplier: 1, ImagePrice1K: &price, ImagePrice2K: &price, ImagePrice4K: &price}, User: &service.User{ID: 100}})
	c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: 100, Concurrency: 0})
	h.Images(c)
	if rec.Code != http.StatusOK {
		for _, entry := range observed.All() {
			t.Log(entry.Message, entry.ContextMap())
		}
	}
	require.Equal(t, []int64{1, 2, 3}, upstream.ids)
	require.Equal(t, []string{"/v1/images/edits", "/v1/images/generations", "/v1/images/generations"}, upstream.paths)
	require.False(t, gjson.ValidBytes(upstream.bodies[0]))
	require.Equal(t, "4k", gjson.GetBytes(upstream.bodies[1], "quality").String())
	require.Len(t, gjson.GetBytes(upstream.bodies[1], "image_urls").Array(), 1)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Len(t, gjson.GetBytes(rec.Body.Bytes(), "data").Array(), 1)
	require.Len(t, usage.logs, 1)
	require.InDelta(t, .05, usage.logs[0].ActualCost, 1e-9)
	require.Equal(t, int64(3), usage.logs[0].AccountID)
	require.NotNil(t, usage.logs[0].UpstreamEndpoint)
	require.Equal(t, "/v1/images/generations", *usage.logs[0].UpstreamEndpoint)
	require.JSONEq(t, string(rawPolicy), mustKongfangPolicyJSON(t, accounts[1].Extra[service.SitePolicyExtraKey]))
}

func mustKongfangPolicyJSON(t *testing.T, policy any) string {
	t.Helper()
	raw, err := json.Marshal(policy)
	require.NoError(t, err)
	return string(raw)
}
