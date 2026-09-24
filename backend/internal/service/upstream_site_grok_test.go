package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type siteGrokAdmin struct{ siteTestAdmin }

func (siteGrokAdmin) GetGroup(context.Context, int64) (*Group, error) {
	return &Group{ID: 9, Platform: PlatformGrok, Status: StatusActive}, nil
}

func TestUpstreamSiteGrokBindingCreatesGrokAccount(t *testing.T) {
	for _, platform := range []string{PlatformGrok, PlatformOpenAI, PlatformGemini} {
		t.Run(platform, func(t *testing.T) {
			svc, repo, accounts := siteTestService()
			svc.admin = siteGrokAdmin{}
			now := time.Now()
			binding := SiteBinding{ID: "pending", GroupID: "up", Model: "grok-imagine-video", LocalModel: "local-video", LocalGroupID: 9, Enabled: true}
			site := &UpstreamSite{ID: "grok", Kind: "sub2api", Enabled: true, LastSuccess: &now, Bindings: []SiteBinding{binding}, Models: []SiteModel{{GroupID: "up", Model: binding.Model, Platform: platform, Tiers: []SitePriceTier{{Key: "default", Unit: "USD/request", Prices: map[string]float64{"request": .01}}}}}}
			require.NoError(t, svc.saveSecret(context.Background(), site, &SiteCredentials{Keys: map[string]string{binding.ID: "test-key"}}))
			updated, err := svc.Bind(context.Background(), site.ID, binding)
			if platform == PlatformGemini {
				require.ErrorContains(t, err, "平台不匹配")
				require.Zero(t, accounts.creates)
				return
			}
			require.NoError(t, err)
			require.Len(t, updated.Bindings, 1)
			account := accounts.accounts[updated.Bindings[0].AccountID]
			require.Equal(t, PlatformGrok, account.Platform)
			require.Equal(t, "grok-imagine-video", account.GetMappedModel("local-video"))
			require.Equal(t, []int64{9}, account.GroupIDs)
			stored, err := repo.Get(context.Background(), site.ID)
			require.NoError(t, err)
			require.NotZero(t, stored.Bindings[0].AccountID)
		})
	}
}

func TestUpstreamSiteGrokVideoPurchaseUnitsAndScheduling(t *testing.T) {
	pricing, groups := siteTestPricing()
	groups.group.Platform = PlatformGrok
	groups.group.RateMultiplier = 5
	groups.group.VideoRateIndependent = true
	groups.group.VideoRateMultiplier = 2
	groups.group.VideoModelPrices = map[string]map[string]float64{"grok-imagine-video": {"480p": .05, "720p": .1}}
	manual := SiteManualPrice{BillingMode: "video", Prices: map[string]float64{"480p": .02, "720p": .3}}
	tiers, err := manual.tiers()
	require.NoError(t, err)
	require.Equal(t, "USD/second", tiers[0].Unit)
	require.Equal(t, map[string]float64{"second": .02}, tiers[0].Prices)
	require.Empty(t, tiers[2].Prices)
	require.NotEmpty(t, tiers[2].Reason)
	p := SiteAccountPolicy{BindingID: "video", LocalGroupID: 9, LocalModel: "grok-imagine-video", UpstreamModel: "grok-imagine-video", Enabled: true, ManualPrice: true, Tiers: tiers}
	account := siteAutomaticAccount(p)
	ctx := context.WithValue(context.Background(), sitePricingKey{}, pricing)
	state := pricing.AccountScheduling(ctx, account)
	require.InDelta(t, .1, state.Tiers[0].Selling["second"], 1e-12)
	for _, tc := range []struct{ resolution, reason string }{{"480p", ""}, {"720p", "site_price_exceeded"}, {"1080p", "site_price_unknown"}, {"4K", "site_price_unknown"}, {"", ""}} {
		request := WithSitePriceRequest(ctx, []byte(fmt.Sprintf(`{"model":"grok-imagine-video","resolution":%q,"duration":6}`, tc.resolution)))
		veto, reason := SitePriceVeto(request, account)
		require.Equal(t, tc.reason != "", veto, tc.resolution)
		require.Equal(t, tc.reason, reason, tc.resolution)
	}
	// A fixed purchase price stays per request; compare with the actual duration's revenue.
	p.Tiers = []SitePriceTier{{Key: "default", Unit: "USD/request", Prices: map[string]float64{"request": .35}}}
	account = siteAutomaticAccount(p)
	for _, seconds := range []int{1, 6} {
		request := WithSitePriceRequest(ctx, []byte(fmt.Sprintf(`{"model":"grok-imagine-video","resolution":"480p","duration":%d}`, seconds)))
		veto, _ := SitePriceVeto(request, account)
		require.Equal(t, seconds == 1, veto)
	}
	_, err = (SiteManualPrice{BillingMode: "video", Prices: map[string]float64{"1K": .1}}).tiers()
	require.Error(t, err)
}

func TestUpstreamSiteGrokVideoCatalogueUsesExplicitBillingMode(t *testing.T) {
	models, err := parseSub2APISiteCatalog(gjson.Parse(`[{"id":1,"rate_multiplier":10,"video_rate_independent":true,"video_rate_multiplier":0.5,"models":[{"name":"grok-imagine-video","platform":"grok","pricing":{"billing_mode":"video","intervals":[{"tier_label":"480p","per_request_price":0.05},{"tier_label":"720p","per_request_price":0.07}]}},{"name":"grok-imagine-video-1.5","platform":"openai","pricing":{"billing_mode":"per_request","per_request_price":0.35}}]}]`), gjson.Parse(`{}`))
	require.NoError(t, err)
	require.Len(t, models, 2)
	require.False(t, models[0].Image)
	require.Equal(t, "USD/second", models[0].Tiers[0].Unit)
	require.Equal(t, .025, models[0].Tiers[0].Prices["second"])
	require.NotEmpty(t, models[0].Tiers[2].Reason)
	require.False(t, models[1].Image)
	require.Equal(t, "USD/request", models[1].Tiers[0].Unit)
	require.Equal(t, 3.5, models[1].Tiers[0].Prices["request"])
	public, err := parsePublicPricingSiteCatalog(gjson.Parse(`{"groups":[{"id":4,"platform":"grok"}],"models":[{"group_id":4,"name":"grok-imagine-video","billing_mode":"video","price_available":true,"video_price_480p":0.02,"video_price_720p":0.03}]}`), gjson.Parse(sitePublicGroups), gjson.Parse(`{}`))
	require.NoError(t, err)
	require.Equal(t, .03, public[0].Tiers[1].Prices["second"])
	require.NotEmpty(t, public[0].Tiers[2].Reason)
	channel, err := siteAuthenticatedChannelModel(
		gjson.Parse(`{"id":4,"platform":"grok","rate_multiplier":0,"video_rate_independent":true,"video_rate_multiplier":0.5,"video_model_prices":{"grok-imagine-video":{"720p":0.03}}}`),
		gjson.Parse(`{"name":"grok-imagine-video","pricing":{"billing_mode":"video","per_request_price":0.05}}`), gjson.Parse(`{}`))
	require.NoError(t, err)
	require.Empty(t, channel.Reason)
	require.Equal(t, .015, channel.Tiers[1].Prices["second"])
}
