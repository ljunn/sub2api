package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
)

type sitePricingGroups struct {
	GroupRepository
	group *Group
	err   error
}

func (g *sitePricingGroups) GetByIDLite(context.Context, int64) (*Group, error) {
	return g.group, g.err
}

type sitePricingRates struct {
	UserGroupRateRepository
	rate *float64
	err  error
}

func (r *sitePricingRates) GetByUserAndGroup(context.Context, int64, int64) (*float64, error) {
	return r.rate, r.err
}
func sitePricePtr(v float64) *float64 { return &v }
func siteTestPricing() (*UpstreamSitePricing, *sitePricingGroups) {
	g := &sitePricingGroups{group: &Group{ID: 9, Status: StatusActive, Platform: PlatformOpenAI, RateMultiplier: 1, ImagePrice1K: sitePricePtr(.15), ImagePrice2K: sitePricePtr(.2), ImagePrice4K: sitePricePtr(.5)}}
	b := NewBillingService(&config.Config{}, nil)
	return NewUpstreamSitePricing(g, b, NewModelPricingResolver(nil, b), &sitePricingRates{}), g
}
func siteAutomaticAccount(p SiteAccountPolicy) *Account {
	raw, _ := json.Marshal(p)
	var policy map[string]any
	_ = json.Unmarshal(raw, &policy)
	return &Account{ID: 31, Status: StatusActive, Schedulable: true, GroupIDs: []int64{9}, Credentials: map[string]any{SiteBindingCredentialKey: p.BindingID}, Extra: map[string]any{SitePolicyExtraKey: policy}}
}
func siteAutomaticPolicy() SiteAccountPolicy {
	return SiteAccountPolicy{LocalGroupID: 9, LocalModel: "local-image", BindingID: "binding", Image: true, Enabled: true, FreshUntil: time.Now().Add(time.Minute), Tiers: []SitePriceTier{{Key: "default", Unit: "USD/image", Prices: map[string]float64{"request": .1}}}}
}
func TestUpstreamSiteAutomaticCeilingsUseSellingMarginAndResolution(t *testing.T) {
	pricing, groups := siteTestPricing()
	groups.group.ProfitControlEnabled = true
	groups.group.ProfitMinMargin = .2
	groups.group.ProfitSafetyBuffer = .1
	groups.group.ImageRateIndependent = true
	groups.group.ImageRateMultiplier = 2
	p := siteAutomaticPolicy()
	p.Limits = []SiteTierLimit{{Key: "default", Unit: "USD/image", Enabled: true, Limits: map[string]float64{"request": 99999}}}
	got := pricing.apply(context.Background(), p, false)
	require.Len(t, got.Limits, 3)
	for i, want := range []float64{.21, .28, .7} {
		require.InDelta(t, want, got.Limits[i].Limits["request"], 1e-10)
		require.Empty(t, got.Limits[i].Reason)
	}
	require.InDelta(t, .3, got.Limits[0].Selling["request"], 1e-10)
	// Disabling a resolution is configuration; numeric client limits are ignored.
	got.Limits[1].Enabled = false
	got.Limits[0].Limits["request"] = 99999
	got = pricing.apply(context.Background(), got, false)
	require.False(t, got.Limits[1].Enabled)
	require.InDelta(t, .21, got.Limits[0].Limits["request"], 1e-10)
}
func TestUpstreamSiteLocalPriceEditAffectsQueuedRequestWithoutScan(t *testing.T) {
	pricing, groups := siteTestPricing()
	p := pricing.apply(context.Background(), siteAutomaticPolicy(), false)
	a := siteAutomaticAccount(p)
	ctx := WithSiteImageSize(context.WithValue(context.Background(), sitePricingKey{}, pricing), "2K")
	require.NoError(t, CheckSitePriceBeforeSend(ctx, a, nil))
	groups.group.ImagePrice2K = sitePricePtr(.05)
	require.Error(t, CheckSitePriceBeforeSend(ctx, a, nil))
	// A stale blocked price snapshot cannot keep an account out of candidate lists.
	stale := pricing.apply(context.Background(), p, false)
	a = siteAutomaticAccount(stale)
	groups.group.ImagePrice2K = sitePricePtr(.2)
	require.True(t, a.siteHasEligibleTier())
	require.NoError(t, CheckSitePriceBeforeSend(ctx, a, nil))
	groups.err = errors.New("database unavailable")
	require.Error(t, CheckSitePriceBeforeSend(ctx, a, nil))
}
func TestUpstreamSiteUserRateAndBillingSnapshotStayConservative(t *testing.T) {
	pricing, groups := siteTestPricing()
	pricing.rates = &sitePricingRates{rate: sitePricePtr(.1)}
	a := siteAutomaticAccount(siteAutomaticPolicy())
	ctx := WithSiteImageSize(context.WithValue(context.WithValue(context.Background(), sitePricingKey{}, pricing), ctxkey.UserID, int64(7)), "2K")
	veto, _ := SitePriceVeto(ctx, a)
	require.True(t, veto)
	groups.group.ImageRateIndependent = true
	groups.group.ImageRateMultiplier = 1
	veto, _ = SitePriceVeto(ctx, a)
	require.False(t, veto, "independent image rate ignores personal token discount")
	snapshot := *groups.group
	snapshot.ImagePrice2K = sitePricePtr(.01)
	ctx = context.WithValue(ctx, ctxkey.Group, &snapshot)
	veto, _ = SitePriceVeto(ctx, a)
	require.True(t, veto, "a stale lower billing price must not be replaced with a higher admission price")
}
func TestUpstreamSiteTokenAliasUsesLocalCardAndBlocksUnpricedComponents(t *testing.T) {
	pricing, groups := siteTestPricing()
	groups.group.RateMultiplier = 2
	groups.group.ModelPricing = []ChannelModelPricing{{Models: []string{"local-text"}, BillingMode: BillingModeToken, InputPrice: sitePricePtr(2e-6), OutputPrice: sitePricePtr(8e-6), CacheReadPrice: sitePricePtr(.2e-6)}}
	p := SiteAccountPolicy{LocalGroupID: 9, LocalModel: "local-text", Enabled: true, FreshUntil: time.Now().Add(time.Minute), Tiers: []SitePriceTier{{Key: "default", Unit: "USD/1M tokens", Prices: map[string]float64{"input_price": 3, "output_price": 12, "cache_read_price": .3}}}}
	got := pricing.apply(context.Background(), p, false)
	require.Empty(t, got.Limits[0].Reason)
	require.InDelta(t, 4, got.Limits[0].Limits["input_price"], 1e-9)
	require.InDelta(t, 16, got.Limits[0].Limits["output_price"], 1e-9)
	require.InDelta(t, .4, got.Limits[0].Limits["cache_read_price"], 1e-9)
	require.Empty(t, siteTierReason(got, "default", time.Now()))
	p.Tiers[0].Prices["audio_input_price"] = 1
	got = pricing.apply(context.Background(), p, false)
	require.NotEmpty(t, siteTierReason(got, "default", time.Now()))
	delete(p.Tiers[0].Prices, "audio_input_price")
	p.LocalModel = "unconfigured-unique-alias"
	got = pricing.apply(context.Background(), p, false)
	require.NotEmpty(t, siteTierReason(got, "default", time.Now()))
}
func TestUpstreamSiteListAndPreviewRefreshDerivedPricesWithoutSaving(t *testing.T) {
	svc, repo, _ := siteTestService()
	pricing, groups := siteTestPricing()
	svc.pricing = pricing
	now := time.Now()
	repo.sites["s"] = &UpstreamSite{ID: "s", Enabled: true, LastSuccess: &now, Models: []SiteModel{{GroupID: "g", Model: "upstream", Image: true, Tiers: siteAutomaticPolicy().Tiers}}, Bindings: []SiteBinding{{ID: "b", GroupID: "g", Model: "upstream", LocalGroupID: 9, LocalModel: "local-image", Enabled: true}}}
	result, err := svc.List(context.Background())
	require.NoError(t, err)
	require.InDelta(t, .2, result[0].Bindings[0].Limits[1].Limits["request"], 1e-9)
	groups.group.ImagePrice2K = sitePricePtr(.4)
	preview, err := svc.PricePreview(context.Background(), "s", repo.sites["s"].Bindings[0])
	require.NoError(t, err)
	require.InDelta(t, .4, preview.Limits[1].Limits["request"], 1e-9)
	require.Empty(t, repo.sites["s"].Bindings[0].Limits, "read-only preview never publishes account policy")
}

func TestUpstreamSiteImageCardOverridesGroupPriceAndPreservesDisabledTier(t *testing.T) {
	pricing, groups := siteTestPricing()
	groups.group.ModelPricing = []ChannelModelPricing{{Models: []string{"local-image"}, BillingMode: BillingModeImage, PerRequestPrice: sitePricePtr(.6), Intervals: []PricingInterval{{TierLabel: "2K", PerRequestPrice: sitePricePtr(.07)}}}}
	p := pricing.apply(context.Background(), siteAutomaticPolicy(), false)
	require.InDelta(t, .07, p.Limits[1].Limits["request"], 1e-9)
	require.Equal(t, "site_price_exceeded", siteTierReason(p, "2K", time.Now()))
	p.Limits[1].Enabled = false
	tiers := p.Tiers
	p.Tiers = nil
	p = pricing.apply(context.Background(), p, false)
	p.Tiers = tiers
	p = pricing.apply(context.Background(), p, false)
	require.False(t, p.Limits[1].Enabled, "temporary catalogue removal must preserve a manual pause")
}
func TestUpstreamSiteImageTokenPriceUsesTokenMultiplier(t *testing.T) {
	pricing, groups := siteTestPricing()
	groups.group.RateMultiplier = .5
	groups.group.ImageRateIndependent = true
	groups.group.ImageRateMultiplier = 9
	groups.group.ModelPricing = []ChannelModelPricing{{Models: []string{"local-image"}, BillingMode: BillingModeToken, InputPrice: sitePricePtr(2e-6), OutputPrice: sitePricePtr(8e-6)}}
	p := siteAutomaticPolicy()
	p.Tiers = []SitePriceTier{{Key: "default", Unit: "USD/1M tokens", Prices: map[string]float64{"input_price": 1, "output_price": 4}}}
	got := pricing.apply(context.Background(), p, false)
	require.InDelta(t, 1, got.Limits[0].Limits["input_price"], 1e-9)
	require.InDelta(t, 4, got.Limits[0].Limits["output_price"], 1e-9)
}
func TestUpstreamSiteRecordUsageBillsTheSameLocalAliasAsAdmission(t *testing.T) {
	pricing, groups := siteTestPricing()
	groups.group.ModelPricing = []ChannelModelPricing{{Models: []string{"local-text"}, BillingMode: BillingModeToken, InputPrice: sitePricePtr(2e-6), OutputPrice: sitePricePtr(8e-6)}}
	p := siteAutomaticPolicy()
	p.Image = false
	p.LocalModel = "local-text"
	account := siteAutomaticAccount(p)
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, nil)
	svc.billingService = pricing.billing
	svc.resolver = pricing.resolver
	input := &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{RequestID: "site-price-alias", Model: "local-text", UpstreamModel: "gpt-5.1", UpstreamResponseModel: "gpt-5.1", Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 1000}},
		APIKey: &APIKey{ID: 1, GroupID: &groups.group.ID, Group: groups.group}, User: &User{ID: 2}, Account: account,
		ChannelUsageFields: ChannelUsageFields{OriginalModel: "local-text", ChannelMappedModel: "gpt-5.1", BillingModelSource: BillingModelSourceResponse},
	}
	require.NoError(t, svc.RecordUsage(context.Background(), input))
	require.NotNil(t, usageRepo.lastLog)
	require.InDelta(t, .01, usageRepo.lastLog.ActualCost, 1e-9, "upstream or channel aliases must not replace local selling prices")
	require.Equal(t, BillingModelSourceResponse, input.BillingModelSource, "do not mutate caller input")
}
