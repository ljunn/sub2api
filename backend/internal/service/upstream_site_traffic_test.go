//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func trafficFixture(t *testing.T) (*UpstreamSitePricing, []Account, context.Context) {
	t.Helper()
	pricing, _ := siteTestPricing()
	accounts := []Account{sitePriorityTestAccount(1, "model-a", .05), sitePriorityTestAccount(2, "model-a", .1)}
	for i := range accounts {
		a := &accounts[i]
		binding := fmt.Sprint(a.ID)
		a.Credentials[SiteBindingCredentialKey] = binding
		a.Credentials["model_mapping"] = map[string]any{"model-a": "model-a"}
		a.Extra[SitePolicyExtraKey].(map[string]any)["binding_id"] = binding
		a.Extra["openai_passthrough"] = true
	}
	base := WithSitePriceRequest(context.WithValue(context.Background(), sitePricingKey{}, pricing), []byte(`{"model":"model-a","size":"2K"}`))
	return pricing, accounts, base
}
func seedTrafficHealth(pricing *UpstreamSitePricing, id int64, n int, seconds float64) {
	for i := 0; i < n; i++ {
		pricing.performance.record(sitePerformanceKey{id, "model-a", "2K"}, sitePerformanceSample{time.Now().Add(time.Duration(i-n) * time.Millisecond), true, seconds})
	}
}
func enableTraffic(pricing *UpstreamSitePricing, percent float64) {
	pricing.support.entries = map[string]siteSupportEntry{"2": {Config: SiteTrafficSupport{Enabled: true, Percent: percent}}}
	pricing.support.until = time.Now().Add(time.Hour)
}
func trafficFirst(ctx context.Context, accounts []Account) (Account, *siteTrafficRequest) {
	ranked := siteEffectivePriorities(ctx, accounts)
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].Priority < ranked[j].Priority })
	r, _ := ctx.Value(siteTrafficRequestKey{}).(*siteTrafficRequest)
	return ranked[0], r
}
func TestSiteTrafficTwentyPercentFirstAttemptsAndNoRetryDoubleCounting(t *testing.T) {
	pricing, accounts, base := trafficFixture(t)
	enableTraffic(pricing, 20)
	seedTrafficHealth(pricing, 1, 30, 5)
	seedTrafficHealth(pricing, 2, 30, 120)
	counts := map[int64]int{}
	for i := 0; i < 100; i++ {
		ctx, close := WithSiteTrafficRequest(base, "/v1/images/generations")
		a, r := trafficFirst(ctx, accounts)
		counts[a.ID]++
		forward, _ := beginSiteForward(ctx, &a)
		markSiteForwardStarted(forward)
		// Simulate the transport retry and a different-account failover in the same request.
		markSiteForwardStarted(forward)
		r.start(ctx, 3-a.ID)
		close()
	}
	require.Equal(t, 20, counts[2])
	require.Equal(t, 80, counts[1])
	p, _ := accounts[1].SitePolicy()
	status := pricing.trafficStatus(base, &accounts[1], p, "2K")
	require.Equal(t, 100, status.Total)
	require.Equal(t, 20, status.FirstAttempts)
	require.Equal(t, 20.0, status.Actual)
}
func TestSiteTrafficColdAccountRecoversWithoutSupportAndAdminReadsArePure(t *testing.T) {
	pricing, accounts, base := trafficFixture(t)
	seedTrafficHealth(pricing, 1, 30, 5)
	for i := 0; i < 30; i++ {
		pricing.AccountScheduling(base, &accounts[1])
		siteEffectivePriorities(base, accounts)
	}
	require.Empty(t, pricing.traffic.memory)
	counts := map[int64]int{}
	for i := 0; i < 40; i++ {
		ctx, close := WithSiteTrafficRequest(base, "/v1/images/generations")
		a, r := trafficFirst(ctx, accounts)
		counts[a.ID]++
		r.start(ctx, a.ID)
		close()
	}
	require.Equal(t, 1, counts[2], "stale account receives an initial recovery request; no immediate repeated probes")
}
func TestSiteTrafficRedisReservationsAreSharedAtomicAndReleased(t *testing.T) {
	mini := miniredis.RunT(t)
	pricing, accounts, base := trafficFixture(t)
	enableTraffic(pricing, 20)
	pricing.traffic.redis = redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { pricing.traffic.redis.Close() })
	seedTrafficHealth(pricing, 1, 30, 5)
	seedTrafficHealth(pricing, 2, 30, 120)
	// A second process uses the same Redis DB, with its own request-local state.
	second, _, base2 := trafficFixture(t)
	enableTraffic(second, 20)
	second.traffic.redis = pricing.traffic.redis
	seedTrafficHealth(second, 1, 30, 5)
	seedTrafficHealth(second, 2, 30, 120)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var requests []*siteTrafficRequest
	var closes []func()
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			current := base
			if i%2 == 0 {
				current = base2
			}
			ctx, close := WithSiteTrafficRequest(current, "/v1/images/generations")
			_, r := trafficFirst(ctx, accounts)
			mu.Lock()
			requests = append(requests, r)
			closes = append(closes, close)
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	support := 0
	for _, r := range requests {
		if r.chosen == 2 && r.planned {
			support++
		}
	}
	require.LessOrEqual(t, support, 4, "concurrent selectors cannot all claim the same support credit")
	require.Greater(t, support, 0)
	for _, close := range closes {
		close()
	}
	key := siteTrafficPoolKey(9, PlatformOpenAI, "model-a", "2K")
	pool, err := pricing.traffic.read(base, key)
	require.NoError(t, err)
	require.Empty(t, pool.Pending)
	require.Empty(t, pool.Buckets, "abandoned selections are not sent requests")
}
func TestSiteTrafficFailoverUsesAnotherAccountInBothSchedulers(t *testing.T) {
	for _, advanced := range []string{"false", "true"} {
		t.Run(advanced, func(t *testing.T) {
			resetOpenAIAdvancedSchedulerSettingCacheForTest()
			defer resetOpenAIAdvancedSchedulerSettingCacheForTest()
			pricing, accounts, base := trafficFixture(t)
			enableTraffic(pricing, 20)
			seedTrafficHealth(pricing, 1, 30, 5)
			seedTrafficHealth(pricing, 2, 30, 120)
			key := siteTrafficPoolKey(9, PlatformOpenAI, "model-a", "2K")
			require.NoError(t, pricing.traffic.update(base, key, func(p *siteTrafficPool) { p.Credit[2] = .8 }))
			ctx, close := WithSiteTrafficRequest(base, "/v1/images/generations")
			defer close()
			svc := &OpenAIGatewayService{accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cfg: &config.Config{}, rateLimitService: newOpenAIAdvancedSchedulerRateLimitService(advanced), concurrencyService: NewConcurrencyService(stubConcurrencyCache{})}
			group := int64(9)
			selected, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "", "model-a", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.Equal(t, int64(2), selected.Account.ID)
			forward, observation := beginSiteForward(ctx, selected.Account)
			markSiteForwardStarted(forward)
			observation.finish(forward, nil, false, false, nil, &UpstreamFailoverError{StatusCode: 503})
			if selected.ReleaseFunc != nil {
				selected.ReleaseFunc()
			}
			selected, _, err = svc.SelectAccountWithScheduler(ctx, &group, "", "", "model-a", map[int64]struct{}{2: {}}, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.Equal(t, int64(1), selected.Account.ID)
			forward, _ = beginSiteForward(ctx, selected.Account)
			markSiteForwardStarted(forward)
			if selected.ReleaseFunc != nil {
				selected.ReleaseFunc()
			}
			status := pricing.trafficStatus(base, &accounts[1], func() SiteAccountPolicy { p, _ := accounts[1].SitePolicy(); return p }(), "2K")
			require.Equal(t, 1, status.Total)
			require.Equal(t, 1, status.FirstAttempts)
		})
	}
}
func TestSiteTrafficHealthDegradesAndRampsBackUp(t *testing.T) {
	var values []sitePerformanceSample
	appendSample := func(ok bool) { values = append(values, sitePerformanceSample{time.Now(), ok, 10}) }
	for i := 0; i < 20; i++ {
		appendSample(true)
	}
	rate, state := siteSupportHealth(values, 20)
	require.Equal(t, 20.0, rate)
	require.Equal(t, "active", state)
	for i := 0; i < 3; i++ {
		appendSample(false)
	}
	rate, state = siteSupportHealth(values, 20)
	require.Zero(t, rate)
	require.Equal(t, "degraded", state)
	for i := 0; i < 3; i++ {
		appendSample(true)
	}
	rate, state = siteSupportHealth(values, 20)
	require.Equal(t, 10.0, rate)
	require.Equal(t, "ramping", state)
	for i := 0; i < 20; i++ {
		appendSample(true)
	}
	rate, state = siteSupportHealth(values, 20)
	require.Equal(t, 20.0, rate)
	require.Equal(t, "active", state)
}
func TestSiteTrafficRespectsPricePauseExpiryAndTierIsolation(t *testing.T) {
	pricing, accounts, base := trafficFixture(t)
	enableTraffic(pricing, 20)
	seedTrafficHealth(pricing, 1, 30, 5)
	seedTrafficHealth(pricing, 2, 30, 120)
	for _, kind := range []string{"price", "paused", "expired"} {
		t.Run(kind, func(t *testing.T) {
			copy := append([]Account(nil), accounts...)
			if kind == "price" {
				copy[1] = sitePriorityTestAccount(2, "model-a", 5)
			}
			if kind == "paused" {
				copy[1].Schedulable = false
			}
			if kind == "expired" {
				past := time.Now().Add(-time.Minute)
				pricing.support.entries["2"] = siteSupportEntry{Config: SiteTrafficSupport{Enabled: true, Percent: 20, ExpiresAt: &past}}
			}
			ctx, close := WithSiteTrafficRequest(base, "/v1/images/generations")
			defer close()
			ranked := siteEffectivePriorities(ctx, copy)
			if kind != "expired" {
				require.False(t, ranked[1].sitePriority)
			} else {
				r := ctx.Value(siteTrafficRequestKey{}).(*siteTrafficRequest)
				for _, c := range r.candidates {
					require.Zero(t, c.rate)
				}
			}
		})
	}
	require.NotEqual(t, siteTrafficPoolKey(9, "openai", "model-a", "1K"), siteTrafficPoolKey(9, "openai", "model-a", "2K"))
}
func TestSiteTrafficPerformanceSurvivesRestartAndKeepsLowWeightHistory(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	defer client.Close()
	first := sitePerformanceStore{redis: client}
	key := sitePerformanceKey{1, "model-a", "2K"}
	first.record(key, sitePerformanceSample{time.Now().Add(-2 * time.Hour), true, 42})
	second := sitePerformanceStore{redis: client}
	require.Empty(t, second.snapshot(key, time.Now()))
	require.Len(t, second.history(key, time.Now()), 1)
	pricing, accounts, _ := trafficFixture(t)
	pricing.performance.redis = client
	p, _ := accounts[0].SitePolicy()
	score := pricing.priorityScore(1, p, "2K", time.Now())
	require.Zero(t, score.Samples)
	require.Greater(t, score.SuccessRate, .5)
	require.Equal(t, 42.0, score.SpeedSeconds)
}

type trafficSettingsRepo struct {
	SettingRepository
	value string
}

func (r *trafficSettingsRepo) GetValue(context.Context, string) (string, error) { return r.value, nil }
func (r *trafficSettingsRepo) Set(_ context.Context, _ string, value string) error {
	r.value = value
	return nil
}
func TestSiteTrafficSettingsSurviveOldSiteSyncAndValidateCombinedQuota(t *testing.T) {
	pricing, _, _ := trafficFixture(t)
	settings := &trafficSettingsRepo{}
	pricing.support.repo = settings
	repo := &siteMemoryRepo{sites: map[string]*UpstreamSite{"one": {ID: "one", Bindings: []SiteBinding{{ID: "a", LocalGroupID: 9, LocalModel: "image", Platform: PlatformOpenAI, AccountID: 1}, {ID: "b", LocalGroupID: 9, LocalModel: "image", Platform: PlatformOpenAI, AccountID: 2}}}}}
	svc := &UpstreamSiteService{repo: repo, pricing: pricing}
	ctx := context.Background()
	require.NoError(t, svc.SaveTrafficSupport(ctx, "one", "a", SiteTrafficSupport{Enabled: true, Percent: 20}))
	require.Error(t, svc.SaveTrafficSupport(ctx, "one", "b", SiteTrafficSupport{Enabled: true, Percent: 80}))
	site, _ := repo.Get(ctx, "one")
	require.NoError(t, repo.Save(ctx, site))
	svc.AnnotateTrafficSupport(ctx, site)
	require.Equal(t, 20.0, site.Bindings[0].TrafficSupport.Percent)
	require.True(t, site.Bindings[0].TrafficSupport.Enabled)
	var registry map[string]siteSupportEntry
	require.NoError(t, json.Unmarshal([]byte(settings.value), &registry))
	require.Len(t, registry, 1)
	for _, percent := range []float64{-1, 0, 96} {
		require.Error(t, svc.SaveTrafficSupport(ctx, "one", "a", SiteTrafficSupport{Enabled: true, Percent: percent}))
	}
}
