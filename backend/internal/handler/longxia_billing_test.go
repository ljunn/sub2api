//go:build unit

package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func (s *grokMediaSlotBindings) ReleaseGrokVideoBilled(_ context.Context, key string) error {
	delete(s.billed, key)
	return nil
}

func TestLongXiaHandlerUsesBoundAccountAndRejectsOtherOwners(t *testing.T) {
	account := service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 50, GroupIDs: []int64{24}, Extra: map[string]any{service.AccountExtraLongXia: true}, Credentials: map[string]any{"api_key": "test-key", "base_url": "https://api8.longxiaai.store"}}
	repo := grokMediaSlotRepo{openAIImagesFailoverAccountRepo: openAIImagesFailoverAccountRepo{accounts: []service.Account{account}}}
	slots := &grokMediaSlotsCache{accounts: map[string]int64{}, users: map[string]int64{}}
	concurrency := service.NewConcurrencyService(slots)
	bindings := &grokMediaSlotBindings{owner: 1}
	upstream := &grokMediaSlotUpstream{call: func(req *http.Request, id int64) (*http.Response, error) {
		require.Equal(t, int64(1), id)
		if req.Method == http.MethodPost {
			require.Equal(t, "/v1/videos", req.URL.Path)
		} else {
			require.Equal(t, "/v1/videos/task_bound", req.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"id":"task_bound","status":"queued"}`))}, nil
	}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	billingSvc := service.NewBillingService(cfg, nil)
	resolver := service.NewModelPricingResolver(nil, billingSvc)
	gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, bindings, cfg, nil, concurrency, billingSvc, nil, nil, upstream, nil, nil, nil, resolver, nil, nil, nil, nil)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	h := NewOpenAIGatewayHandler(gateway, concurrency, billing, service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
	newContext := func(create bool) (*gin.Context, *httptest.ResponseRecorder) {
		c, w := grokMediaSlotContext(context.Background(), create)
		key, _ := middleware.GetAPIKeyFromContext(c)
		key.Group.Platform = service.PlatformOpenAI
		price := 0.2
		key.Group.ModelPricing = []service.ChannelModelPricing{{Models: []string{"LongXia-video-seedance2_5-standard-720p-express"}, BillingMode: service.BillingModeVideo, PerRequestPrice: &price}}
		if create {
			c.Request = httptest.NewRequest(http.MethodPost, "/api/v3/contents/generations/tasks", strings.NewReader(`{"model":"LongXia-video-seedance2_5-standard-720p-express","content":[{"type":"text","text":"waves"}],"duration":25}`))
		}
		c.Params = gin.Params{{Key: "task_id", Value: "longxia:task_bound"}}
		return c, w
	}
	c, w := newContext(true)
	h.SeedanceTasks(c)
	require.Equal(t, 200, w.Code, w.Body.String())
	require.Len(t, bindings.pending, 1)
	slots.assertReleased(t)
	for _, other := range []string{"", "user", "key", "group", "task"} {
		c, w = newContext(false)
		key, _ := middleware.GetAPIKeyFromContext(c)
		switch other {
		case "user":
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 11, Concurrency: 5})
		case "key":
			key.ID = 21
		case "group":
			gid := int64(25)
			key.GroupID = &gid
		case "task":
			c.Params = gin.Params{{Key: "task_id", Value: "longxia:other"}}
		}
		before := upstream.calls
		h.SeedanceTasks(c)
		if other == "" {
			require.Equal(t, 200, w.Code, w.Body.String())
		} else {
			require.Equal(t, 404, w.Code, other+": "+w.Body.String())
			require.Equal(t, before, upstream.calls)
		}
		slots.assertReleased(t)
	}
}

func TestLongXiaCompletionBillingOnceAndOnlyAfterSuccess(t *testing.T) {
	h, _, bindings, _ := newGrokMediaSlotHandler(t, false, false, service.PlatformOpenAI)
	c, _ := grokMediaSlotContext(context.Background(), false)
	key, _ := middleware.GetAPIKeyFromContext(c)
	subject, _ := middleware.GetAuthSubjectFromContext(c)
	id := "seedance:longxia:task_25_seconds"
	result := &service.OpenAIForwardResult{ResponseID: id, VideoCount: 1}
	require.Nil(t, prepareSeedanceCompletionBilling(context.Background(), h, key, subject, id, result), "missing create metadata must not burn the claim")
	require.Empty(t, bindings.billed)
	require.NoError(t, h.gatewayService.StoreGrokVideoPendingBilling(context.Background(), id, subject.UserID, key.ID, service.GrokVideoPendingBilling{
		Model: "video", BillingModel: "video", UpstreamModel: "LongXia-video-seedance2_5-standard-720p-express-PerSecond", VideoDurationSeconds: 25, VideoResolution: "720p",
	}))
	for range 3 {
		pending := &service.OpenAIForwardResult{ResponseID: id}
		require.Nil(t, prepareSeedanceCompletionBilling(context.Background(), h, key, subject, id, pending), "queued and failed tasks are not billable")
	}
	require.Empty(t, bindings.billed)
	bill := prepareSeedanceCompletionBilling(context.Background(), h, key, subject, id, result)
	require.NotNil(t, bill)
	require.Equal(t, 25, bill.VideoDurationSeconds)
	require.Equal(t, "720p", bill.VideoResolution)
	require.Equal(t, "video", bill.BillingModel)
	require.Zero(t, bill.Usage.OutputTokens)
	require.Equal(t, service.StableGrokVideoBillingRequestID(id), bill.RequestID)
	for range 20 {
		require.Nil(t, prepareSeedanceCompletionBilling(context.Background(), h, key, subject, id, result))
	}
	require.Len(t, bindings.billed, 1)
	// A durable usage write failure releases the claim through the existing
	// recorder, allowing a later observation of the same task to retry billing.
	require.NoError(t, h.gatewayService.ReleaseGrokVideoBilling(context.Background(), id, subject.UserID, key.ID))
	require.NotNil(t, prepareSeedanceCompletionBilling(context.Background(), h, key, subject, id, result))
}
