//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const vividSiteCatalogue = `[{"code":"image-one","enabled":true,"kind":"image","qualities":["1K"],"prices":[{"quality":"1K","price":2,"agentPrice":1}]},{"code":"video-one","enabled":true,"kind":"video","qualities":["720p"],"durationMode":"options","durationOptions":[15],"prices":[{"quality":"720p","price":14}]},{"code":"web-text","enabled":true,"kind":"text","qualities":["按次"],"prices":[{"quality":"按次","price":1}]}]`
const vividSiteModels = `[{"id":"image-one"},{"id":"video-one"},{"id":"web-text"}]`

func TestVividAISiteLifecycleReusesKeyAndSyncsPriceBalanceConcurrency(t *testing.T) {
	ctx := context.Background()
	key, limit, fail := "vk_existing", 3, false
	requests := []string{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.Path)
		if r.URL.Path == "/api/cell" {
			require.Empty(t, r.Header.Get("Authorization"))
			var payload map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
			require.Equal(t, "modelSvc", payload["serviceName"])
			require.Equal(t, "publicList", payload["methodName"])
			fmt.Fprintf(w, `{"status":0,"data":{"data":%s}}`, vividSiteCatalogue)
			return
		}
		require.Equal(t, "Bearer "+key, r.Header.Get("Authorization"))
		switch r.URL.Path {
		case "/v1/balance":
			fmt.Fprintf(w, `{"object":"user.balance","balance":36790,"running":0,"concurrency_limit":%d}`, limit)
		case "/v1/models":
			if fail {
				w.WriteHeader(503)
				return
			}
			fmt.Fprintf(w, `{"object":"list","data":%s}`, vividSiteModels)
		default:
			t.Errorf("unexpected mutation/request: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer upstream.Close()
	svc, repo, accounts := siteTestService()
	input := SiteInput{Name: "Vivid", Kind: "vividai", BaseURL: upstream.URL, AuthMode: "token", AccessToken: key, Enabled: true, BalanceUnitsPerUSD: sitePricePtr(1000)}
	site, err := svc.Save(ctx, "", input)
	require.NoError(t, err)
	site, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.Empty(t, site.Error)
	require.Len(t, site.Models, 2)
	require.Equal(t, 3, site.Concurrency.Limit)
	require.Equal(t, "concurrency_limit", site.Concurrency.Source)
	image := findSiteModel(site, "image", "image-one")
	require.NotNil(t, image)
	require.InDelta(t, .002, image.Tiers[0].Prices["request"], 1e-12)
	require.NotEmpty(t, image.Tiers[1].Reason)
	for _, alias := range []string{"local-image", "other-image"} {
		site, err = svc.Bind(ctx, site.ID, SiteBinding{GroupID: "image", Model: "image-one", LocalGroupID: 9, LocalModel: alias, Enabled: true})
		require.NoError(t, err)
	}
	require.Len(t, site.Bindings, 2)
	for _, b := range site.Bindings {
		a := accounts.accounts[b.AccountID]
		require.True(t, a.IsVividAI())
		require.Equal(t, key, a.GetCredential("api_key"))
		require.Equal(t, 3, a.Concurrency)
		require.Equal(t, "partial", b.Status)
	}
	key, limit = "vk_rotated", 2
	input.AccessToken = key
	site, err = svc.Save(ctx, site.ID, input)
	require.NoError(t, err)
	for _, b := range site.Bindings {
		require.Equal(t, key, accounts.accounts[b.AccountID].GetCredential("api_key"))
		require.Equal(t, "blocked", b.Status)
	}
	site, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.Equal(t, 2, accounts.accounts[site.Bindings[0].AccountID].Concurrency)
	creds, err := svc.credentials(site)
	require.NoError(t, err)
	amount, currency, err := newSiteAdapter(site, creds).balance(ctx)
	require.NoError(t, err)
	require.Equal(t, 36790.0, amount)
	require.Equal(t, "积分", currency)
	site.Balance = &SiteBalance{Amount: &amount, Currency: currency}
	convertSiteBalanceUSD(site)
	require.InDelta(t, 36.79, *site.Balance.AmountUSD, 1e-9)
	raw, _ := json.Marshal(site)
	require.NotContains(t, string(raw), key)
	// Sync failures retain the last verified catalog and limits.
	fail = true
	site, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.NotEmpty(t, site.Error)
	require.Len(t, site.Models, 2)
	require.Equal(t, 2, site.Concurrency.Limit)
	_, err = svc.SaveManualPrice(ctx, site.ID, SiteManualPriceInput{SiteManualPrice: SiteManualPrice{GroupID: "video", Model: "video-one", BillingMode: "per_request", Prices: map[string]float64{"request": .01}}})
	require.Error(t, err)
	require.NotEmpty(t, repo.sites[site.ID].Secret)
	for _, request := range requests {
		require.Contains(t, []string{"GET /v1/balance", "GET /v1/models", "POST /api/cell"}, request)
	}
}

func TestVividAISiteRejectsLoginAndInvalidKey(t *testing.T) {
	for _, input := range []SiteInput{
		{AuthMode: "password", Username: "user", Password: "pass"}, {AuthMode: "token", AccessToken: "console-token"}, {AuthMode: "token", RefreshToken: "refresh"},
	} {
		svc, _, _ := siteTestService()
		input.Kind = "vividai"
		input.Name = "Vivid"
		input.BaseURL = "https://example.test"
		_, err := svc.Save(context.Background(), "", input)
		require.Error(t, err)
	}
}

func TestVividAISiteCatalogueAndRequestPriceGuards(t *testing.T) {
	models := parseVividAICatalog(gjson.Parse(vividSiteModels), gjson.Parse(vividSiteCatalogue), .001)
	require.Len(t, models, 2)
	now := time.Now()
	for _, m := range models {
		p := SiteAccountPolicy{SiteKind: "vividai", VividAI: m.VividAI, Image: m.Image, Enabled: true, FreshUntil: now.Add(time.Minute), Tiers: m.Tiers}
		for _, tier := range m.Tiers {
			p.Limits = append(p.Limits, SiteTierLimit{Key: tier.Key, Unit: tier.Unit, Enabled: true, Limits: map[string]float64{"request": 1, "second": 1}})
		}
		if m.Image {
			for _, tc := range []struct {
				body string
				veto bool
			}{{`{}`, false}, {`{"size":"1K"}`, false}, {`{"size":"1K","quality":"2K"}`, true}, {`{"size":"4K"}`, true}} {
				ctx := WithSitePriceRequest(context.Background(), []byte(tc.body))
				veto, _ := vividAISitePriceVeto(p, ctx.Value(siteRequestKey{}).(SitePriceRequest))
				require.Equal(t, tc.veto, veto, tc.body)
			}
			p.Tiers = []SitePriceTier{{Key: "default", Unit: "USD/request", Prices: map[string]float64{"request": .001}}}
			p.Limits = []SiteTierLimit{{Key: "default", Unit: "USD/request", Enabled: true, Limits: map[string]float64{"request": 1}}}
			veto, _ := vividAISitePriceVeto(p, SitePriceRequest{VividAITier: "4K"})
			require.True(t, veto, "manual pricing cannot invent a supported resolution")
		} else {
			require.InDelta(t, .014, m.Tiers[0].Prices["second"], 1e-12)
			require.Equal(t, "USD/second", m.Tiers[0].Unit)
			require.Equal(t, 14000.0, m.Tiers[0].CreditUnits)
			for _, d := range []int64{0, 15, 8, -1, 30} {
				veto, _ := vividAISitePriceVeto(p, SitePriceRequest{VividAITier: "720p", VividAIDuration: d})
				require.Equal(t, d != 0 && d != 15, veto)
			}
		}
	}
	unpriced := parseVividAICatalog(gjson.Parse(vividSiteModels), gjson.Parse(vividSiteCatalogue), 0)
	require.NotEmpty(t, unpriced[0].Reason)
	require.Empty(t, unpriced[0].Tiers[0].Prices)
}

func TestVividAIVideoCostUsesExplicitCreditBillingPrice(t *testing.T) {
	pricing, groups := siteTestPricing()
	m := parseVividAICatalog(gjson.Parse(vividSiteModels), gjson.Parse(vividSiteCatalogue), .001)[1]
	p := SiteAccountPolicy{LocalGroupID: 9, LocalModel: "local-video", SiteKind: "vividai", VividAI: m.VividAI, Enabled: true, FreshUntil: time.Now().Add(time.Minute), Tiers: m.Tiers}
	groups.group.ModelPricing = []ChannelModelPricing{{Models: []string{"local-video"}, BillingMode: BillingModeToken, InputPrice: sitePricePtr(0), OutputPrice: sitePricePtr(2e-6)}}
	got := pricing.apply(context.Background(), p, false)
	require.InDelta(t, .028, got.Limits[0].Limits["second"], 1e-12)
	require.Empty(t, siteTierReason(got, "720p", time.Now()))
	groups.group.ModelPricing[0].OutputPrice = sitePricePtr(.5e-6)
	got = pricing.apply(context.Background(), p, false)
	require.Equal(t, "site_price_exceeded", siteTierReason(got, "720p", time.Now()))
	groups.group.ModelPricing = []ChannelModelPricing{{Models: []string{"local-video"}, BillingMode: BillingModePerRequest, PerRequestPrice: sitePricePtr(.3)}}
	got = pricing.apply(context.Background(), p, false)
	require.InDelta(t, .02, got.Limits[0].Limits["second"], 1e-12)
	groups.group.ModelPricing = nil
	got = pricing.apply(context.Background(), p, false)
	require.NotEmpty(t, got.Limits[0].Reason)
}

func TestVividAIVideoPerSecondPriceMatchesCompletionBilling(t *testing.T) {
	pricing, groups := siteTestPricing()
	m := parseVividAICatalog(gjson.Parse(vividSiteModels), gjson.Parse(vividSiteCatalogue), .01)[1]
	groups.group.ModelPricing = []ChannelModelPricing{{Models: []string{"seedance-2.0"}, BillingMode: BillingModeVideo, PerRequestPrice: sitePricePtr(.2)}}
	groups.group.PeakRateEnabled, groups.group.PeakStart, groups.group.PeakEnd, groups.group.PeakRateMultiplier = true, "00:00", "23:59", 10
	p := SiteAccountPolicy{LocalGroupID: 9, LocalModel: "seedance-2.0", SiteKind: "vividai", VividAI: m.VividAI, Enabled: true, FreshUntil: time.Now().Add(time.Minute), Tiers: m.Tiers}
	got := pricing.apply(context.Background(), p, false)
	require.Equal(t, "USD/second", got.Limits[0].Unit)
	require.InDelta(t, .14, got.Tiers[0].Prices["second"], 1e-12)
	require.InDelta(t, .2, got.Limits[0].Selling["second"], 1e-12)
	require.Empty(t, got.Limits[0].Reason)
	require.Empty(t, siteTierReason(got, "720p", time.Now()))

	// The same card and resolution must charge 15 seconds, not one request.
	svc := &OpenAIGatewayService{billingService: pricing.billing, resolver: pricing.resolver}
	key := &APIKey{GroupID: &groups.group.ID, Group: groups.group}
	result := &OpenAIForwardResult{ResponseID: "seedance:vividai:test", VideoDurationSeconds: 15, VideoResolution: "720p"}
	cost, err := svc.calculateOpenAIRecordUsageCost(context.Background(), result, key, []string{"seedance-2.0"}, 10, 1, 1, 1, UsageTokens{OutputTokens: 210000}, "", nil, time.Now())
	require.NoError(t, err)
	require.InDelta(t, 3, cost.ActualCost, 1e-12)
	require.Equal(t, string(BillingModeVideo), cost.BillingMode)
	require.Equal(t, 1, result.VideoCount)
	result.VideoDurationSeconds = 0
	_, err = svc.calculateOpenAIRecordUsageCost(context.Background(), result, key, []string{"seedance-2.0"}, 1, 1, 1, 1, UsageTokens{}, "", nil, time.Now())
	require.Error(t, err, "missing duration must never be priced as one second or a default duration")

	groups.group.ModelPricing[0].Intervals = []PricingInterval{{TierLabel: "720p", PerRequestPrice: sitePricePtr(.1)}}
	got = pricing.apply(context.Background(), p, false)
	require.InDelta(t, .1, got.Limits[0].Selling["second"], 1e-12)
	require.Equal(t, "site_price_exceeded", siteTierReason(got, "720p", time.Now()))
	groups.group.VideoRateIndependent, groups.group.VideoRateMultiplier = true, 2
	pricing.rates = &sitePricingRates{rate: sitePricePtr(.01)}
	ctx := context.WithValue(context.Background(), ctxkey.UserID, int64(7))
	got = pricing.apply(ctx, p, true)
	require.InDelta(t, .2, got.Limits[0].Selling["second"], 1e-12)
	require.Empty(t, siteTierReason(got, "720p", time.Now()))
	groups.group.VideoRateIndependent = false
	got = pricing.apply(ctx, p, true)
	require.InDelta(t, .001, got.Limits[0].Selling["second"], 1e-12)
	require.Equal(t, "site_price_exceeded", siteTierReason(got, "720p", time.Now()))
}

func TestVividAILegacyTaskQuoteConvertsWithoutMutatingCache(t *testing.T) {
	pricing, groups := siteTestPricing()
	groups.group.ModelPricing = []ChannelModelPricing{{Models: []string{"seedance-2.0"}, BillingMode: BillingModeVideo, PerRequestPrice: sitePricePtr(.2)}}
	tiers := []SitePriceTier{{Key: "720p", Unit: "USD/request", Prices: map[string]float64{"request": 2.1}, CreditUnits: 210000}}
	p := SiteAccountPolicy{LocalGroupID: 9, LocalModel: "seedance-2.0", VividAI: &SiteVividAIModel{Kind: "video", DurationMode: "options", DurationOptions: []int64{15}}, Tiers: tiers, Limits: []SiteTierLimit{{Key: "720p", Enabled: false}}}
	got := pricing.apply(context.Background(), p, false)
	require.InDelta(t, .14, got.Tiers[0].Prices["second"], 1e-12)
	require.InDelta(t, .2, got.Limits[0].Selling["second"], 1e-12)
	require.False(t, got.Limits[0].Enabled)
	require.Equal(t, "USD/request", tiers[0].Unit)
	require.Equal(t, 2.1, tiers[0].Prices["request"])
}

type vividGateCache struct {
	ConcurrencyCache
	mu   sync.Mutex
	held map[int64]bool
}

func (c *vividGateCache) AcquireAccountSlot(_ context.Context, id int64, _ int, _ string) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.held[id] {
		return false, nil
	}
	c.held[id] = true
	return true, nil
}
func (c *vividGateCache) ReleaseAccountSlot(_ context.Context, id int64, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.held, id)
	return nil
}

func TestVividAISiteSharedConcurrencyCountsAcceptedVideoJobs(t *testing.T) {
	cache := &vividGateCache{held: map[int64]bool{}}
	running := 0
	upstream := &vividAIUpstream{call: func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/prefix/v1/balance" {
			return vividAIResponse(200, fmt.Sprintf(`{"object":"user.balance","running":%d,"concurrency_limit":3}`, running)), nil
		}
		if r.URL.Path == "/prefix/v1/generate" {
			running++
			return vividAIResponse(200, fmt.Sprintf("\n{\"jobId\":\"j%d\",\"status\":\"running\"}\n", running)), nil
		}
		return nil, fmt.Errorf("unexpected endpoint %s", r.URL.Path)
	}}
	svc := newOpenAIImagesTestService(upstream)
	svc.concurrencyService = NewConcurrencyService(cache)
	var wg sync.WaitGroup
	outcomes := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			a := vividAIAccount()
			a.ID = int64(i + 1)
			release, err := svc.acquireVividAICreate(context.Background(), a)
			if err == nil {
				resp, e := upstream.Do(httptest.NewRequest(http.MethodPost, "https://vivid.example/prefix/v1/generate", nil), "", a.ID, 3)
				err = e
				if err == nil {
					err = vividAIReadReceipt(resp)
					_, _ = io.ReadAll(resp.Body)
					resp.Body.Close()
				}
				release()
			}
			outcomes <- err
		}(i)
	}
	wg.Wait()
	close(outcomes)
	success := 0
	for err := range outcomes {
		if err == nil {
			success++
		}
	}
	require.Equal(t, 3, success)
	require.Equal(t, 3, running)
	require.Empty(t, cache.held)
	a, b := vividAIAccount(), vividAIAccount()
	b.ID = 100
	b.Credentials["base_url"] = "https://vivid.example/prefix"
	require.Equal(t, vividAICreateGateID(a), vividAICreateGateID(b))
	b.Credentials["api_key"] = "different"
	require.NotEqual(t, vividAICreateGateID(a), vividAICreateGateID(b))
}

func TestVividAISiteAcceptedTaskReadableAfterPriceExpires(t *testing.T) {
	a := vividAIAccount()
	a.Status = StatusActive
	a.Schedulable = true
	a.Concurrency = 3
	a.GroupIDs = []int64{9}
	a.Credentials[SiteBindingCredentialKey] = "binding"
	a.Extra[SitePolicyExtraKey] = map[string]any{"binding_id": "binding", "site_kind": "vividai", "enabled": false}
	repo := &siteTestAccounts{accounts: map[int64]*Account{a.ID: a}}
	svc := newOpenAIImagesTestService(&vividAIUpstream{call: func(r *http.Request) (*http.Response, error) {
		require.Equal(t, "/prefix/v1/generate", r.URL.Path)
		raw, _ := io.ReadAll(r.Body)
		require.JSONEq(t, `{"jobId":"accepted-job"}`, string(raw))
		return vividAIResponse(200, `{"jobId":"accepted-job","status":"succeeded","result":"https://cdn.example/video.mp4","creditsCharged":210}`), nil
	}})
	svc.accountRepo = repo
	group := int64(9)
	selection, _, err := svc.SelectMediaVideoRequestAccount(context.Background(), &group, "verified-owner", a.ID, "video", PlatformOpenAI)
	require.NoError(t, err)
	require.NotNil(t, selection)
	admissionCtx := ContextWithSelectionProfitGate(context.Background(), selection)
	_, vetoed, _ := svc.ProfitControlVetoLatest(admissionCtx, selection.Account)
	require.False(t, vetoed, "handler admission must also allow an accepted task lookup")
	vetoed, _ = SitePriceVeto(context.Background(), selection.Account)
	require.True(t, vetoed, "ordinary creation still checks the disabled policy")
	if selection.ReleaseFunc != nil {
		defer selection.ReleaseFunc()
	}
	c, rec := newOpenAIImagesTestContext(t, nil)
	result, err := svc.ForwardSeedance(context.Background(), c, selection.Account, SeedanceEndpointStatus, "seedance:vividai:accepted-job", nil)
	require.NoError(t, err)
	require.Equal(t, 210000, result.Usage.OutputTokens)
	require.Contains(t, rec.Body.String(), "video_url")
	wrongGroup := int64(88)
	_, _, err = svc.SelectMediaVideoRequestAccount(context.Background(), &wrongGroup, "verified-owner", a.ID, "video", PlatformOpenAI)
	require.Error(t, err)
}
