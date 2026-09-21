//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func sitePriorityTestAccount(id int64, model string, price float64) Account {
	a := siteSchedulerAccount(id, price)
	a.Priority = 50
	p, _ := a.SitePolicy()
	p.LocalModel = model
	raw, _ := json.Marshal(p)
	var policy map[string]any
	_ = json.Unmarshal(raw, &policy)
	a.Extra[SitePolicyExtraKey] = policy
	return a
}

func TestSitePriorityModelsAndSizesAreIndependentAndBackupsStayEnabled(t *testing.T) {
	pricing, _ := siteTestPricing()
	ctx := WithSiteImageSize(context.WithValue(context.Background(), sitePricingKey{}, pricing), "2K")
	accounts := []Account{
		sitePriorityTestAccount(1, "model-a", .02),
		sitePriorityTestAccount(2, "model-a", .1),
		sitePriorityTestAccount(3, "model-b", .01),
		sitePriorityTestAccount(4, "model-a", .12),
		sitePriorityTestAccount(5, "model-a", .13),
		sitePriorityTestAccount(6, "model-a", .14),
	}
	ranked := siteEffectivePriorities(ctx, accounts)
	require.Equal(t, 200, ranked[0].Priority)
	require.Equal(t, 200, ranked[2].Priority, "each model has its own leader")
	require.GreaterOrEqual(t, ranked[1].Priority, 1000)
	for i := range ranked {
		require.True(t, ranked[i].Schedulable, "low-ranked backups remain available")
		require.Equal(t, 50, accounts[i].Priority, "repository snapshots must not be mutated")
		require.False(t, accounts[i].sitePriority)
	}
	now := time.Now()
	for i := 0; i < 20; i++ {
		pricing.performance.record(sitePerformanceKey{1, "model-a", "2K"}, sitePerformanceSample{now, true, 80})
		pricing.performance.record(sitePerformanceKey{2, "model-a", "2K"}, sitePerformanceSample{now, true, 5})
	}
	ranked = siteEffectivePriorities(ctx, accounts)
	require.Equal(t, 200, ranked[1].Priority, "a faster reliable account can beat a cheaper slow account")
	require.Equal(t, 200, ranked[2].Priority, "other models are unaffected")
	before := ranked[1].Priority
	for i := 0; i < 80; i++ {
		pricing.performance.record(sitePerformanceKey{2, "model-a", "4K"}, sitePerformanceSample{now, false, 0})
		pricing.performance.record(sitePerformanceKey{2, "different-model", "2K"}, sitePerformanceSample{now, false, 0})
	}
	require.Equal(t, before, siteEffectivePriorities(ctx, accounts)[1].Priority)
	for i := 0; i < 80; i++ {
		pricing.performance.record(sitePerformanceKey{2, "model-a", "2K"}, sitePerformanceSample{now, false, 0})
	}
	ranked = siteEffectivePriorities(ctx, accounts)
	require.NotEqual(t, 200, ranked[1].Priority, "fast failures must lower the score")
	p, _ := accounts[1].SitePolicy()
	score := pricing.priorityScore(2, pricing.apply(ctx, p, true), "2K", time.Now())
	require.Equal(t, 100, score.Samples)
	require.Equal(t, 20, score.Successes)
	require.Equal(t, 5.0, score.SpeedSeconds, "failed response times do not improve speed")
}

func TestSitePriorityWindowColdStartAndPriceGate(t *testing.T) {
	pricing, groups := siteTestPricing()
	ctx := WithSiteImageSize(context.WithValue(context.Background(), sitePricingKey{}, pricing), "2K")
	account := sitePriorityTestAccount(1, "model-a", .1)
	p, _ := account.SitePolicy()
	p = pricing.apply(ctx, p, true)
	now := time.Now()
	pricing.performance.record(sitePerformanceKey{1, "model-a", "2K"}, sitePerformanceSample{now.Add(-2 * time.Hour), false, 0})
	score := pricing.priorityScore(1, p, "2K", now)
	require.Zero(t, score.Samples)
	require.Equal(t, .5, score.SuccessRate)
	require.True(t, score.SpeedEstimated)
	require.Equal(t, 30.0, score.SpeedReference)
	for i := 0; i < 250; i++ {
		pricing.performance.record(sitePerformanceKey{1, "model-a", "2K"}, sitePerformanceSample{now, true, 10})
	}
	score = pricing.priorityScore(1, p, "2K", now)
	require.Equal(t, 200, score.Samples)
	require.False(t, score.SpeedEstimated)
	require.Less(t, score.SuccessRate, 1.0)
	groups.group.ImagePrice2K = sitePricePtr(.01)
	ranked := siteEffectivePriorities(ctx, []Account{account})
	require.False(t, ranked[0].sitePriority, "a score cannot bypass price admission")
	veto, _ := SitePriceVeto(ctx, &ranked[0])
	require.True(t, veto)
	for _, value := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		require.Equal(t, 25000, sitePriorityForScore(value))
	}
}

func TestSitePriorityAdvancedSchedulerRetainsAndOrdersEveryBackup(t *testing.T) {
	pricing, _ := siteTestPricing()
	ctx := WithSiteImageSize(context.WithValue(context.Background(), sitePricingKey{}, pricing), "2K")
	accounts := siteEffectivePriorities(ctx, []Account{
		sitePriorityTestAccount(1, "model-a", .15),
		sitePriorityTestAccount(2, "model-a", .05),
		sitePriorityTestAccount(3, "model-a", .1),
	})
	plan := openAIAccountLoadPlan{topK: 1}
	for i := range accounts {
		plan.candidates = append(plan.candidates, openAIAccountCandidateScore{account: &accounts[i], loadInfo: &AccountLoadInfo{}, score: float64(100 - i)})
	}
	scheduler := &defaultOpenAIAccountScheduler{}
	order := scheduler.buildOpenAISelectionOrder(OpenAIAccountScheduleRequest{}, plan)
	require.Len(t, order, 3, "Top-K must not remove available managed backups")
	require.Equal(t, int64(2), order[0].account.ID)
	require.Equal(t, int64(3), order[1].account.ID)
	require.Equal(t, int64(1), order[2].account.ID)
}

func TestSitePriorityObserversIgnoreLocalErrorsCancellationAndNestedConversions(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pricing, _ := siteTestPricing()
	account := sitePriorityTestAccount(1, "model-a", .1)
	base := WithSiteImageSize(context.WithValue(context.Background(), sitePricingKey{}, pricing), "2K")
	key := sitePerformanceKey{1, "model-a", "2K"}
	ctx, observation := beginSiteForward(base, &account)
	observation.finish(ctx, nil, false, false, nil, errors.New("local validation"))
	require.Empty(t, pricing.performance.snapshot(key, time.Now()))
	_, nested := beginSiteForward(ctx, &account)
	require.Nil(t, nested, "protocol conversion must not double-count the attempt")
	markSiteForwardStarted(ctx)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Status(400)
	observation.finish(ctx, c, false, false, nil, errors.New("invalid prompt"))
	require.Empty(t, pricing.performance.snapshot(key, time.Now()))
	observation.finish(ctx, c, false, false, nil, &UpstreamFailoverError{StatusCode: 400})
	require.Len(t, pricing.performance.snapshot(key, time.Now()), 1, "retry-later failures count even when upstream uses 400")
	cancelCtx, cancel := context.WithCancel(base)
	cancelCtx, canceled := beginSiteForward(cancelCtx, &account)
	markSiteForwardStarted(cancelCtx)
	cancel()
	canceled.finish(cancelCtx, nil, false, false, nil, context.Canceled)
	require.Len(t, pricing.performance.snapshot(key, time.Now()), 1)
}

func TestSitePriorityForwardImagesRecordsCompletedImageAndKeepsSize(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pricing, _ := siteTestPricing()
	account := kongfangForwardAccount(PlatformOpenAI)
	account.Credentials["model_mapping"] = map[string]any{"local-image": "gpt-image-2"}
	transport := &kongfangForwardTransport{response: `{"created":1,"data":[{"b64_json":"aGVsbG8="}]}`}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: transport}
	body := []byte(`{"model":"local-image","prompt":"test","size":"2560x1440"}`)
	ctx := WithSitePriceRequest(context.WithValue(context.Background(), sitePricingKey{}, pricing), body)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	parsed := &OpenAIImagesRequest{Endpoint: "/v1/images/generations", ContentType: "application/json", N: 1}
	require.NoError(t, parseOpenAIImagesJSONRequest(body, parsed))
	parsed.SizeTier = normalizeOpenAIImageSizeTier(parsed.Size)
	result, err := svc.ForwardImages(ctx, c, account, body, parsed, "")
	require.NoError(t, err)
	require.Equal(t, 1, result.ImageCount)
	samples := pricing.performance.snapshot(sitePerformanceKey{account.ID, "local-image", "2K"}, time.Now())
	require.Len(t, samples, 1)
	require.True(t, samples[0].Success)
	require.Empty(t, pricing.performance.snapshot(sitePerformanceKey{account.ID, "gpt-image-2", "2K"}, time.Now()), "mapped upstream names must not mix local model pools")
}
