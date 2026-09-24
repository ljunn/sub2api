package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Some upstreams sell image generation per request while the local group sells
// it per resolution. Exercise discovery through the actual scheduling gate.
func TestUpstreamSitePerRequestImageScheduling(t *testing.T) {
	for _, source := range []string{"plaza", "public", "channel", "newapi fixed"} {
		names := []string{"gemini-3.1-flash-image-preview", "gpt-image-2", "text-model"}
		if source == "newapi fixed" {
			names = append(names, "gemini-3.0-pro-image")
		}
		for _, name := range names {
			t.Run(source+"/"+name, func(t *testing.T) {
				group := gjson.Parse(`{"id":140,"platform":"gemini","status":"active","rate_multiplier":2,"image_rate_independent":true,"image_rate_multiplier":0}`)
				rates := gjson.Parse(`{"140":0.5}`)
				model := gjson.Parse(fmt.Sprintf(`{"name":%q,"platform":"gemini","pricing":{"billing_mode":"per_request","per_request_price":0.1}}`, name))
				var m SiteModel
				switch source {
				case "newapi fixed":
					endpoints := `[]`
					if name != "text-model" {
						endpoints = `["image-generation"]`
					}
					data := gjson.Parse(fmt.Sprintf(`{"data":[{"model_name":%q,"enable_groups":["flow/as分组"],"supported_endpoint_types":%s,"billing_mode":"tiered_expr","billing_expr":"tier(\"base\", fixed(0.04))","quota_type":0,"model_price":0,"model_ratio":37.5,"completion_ratio":4}],"group_ratio":{"flow/as分组":1.25},"usable_group":{"flow/as分组":"默认分组"}}`, name, endpoints))
					models, err := parseNewAPISiteCatalog(data)
					require.NoError(t, err)
					require.Len(t, models, 1)
					m = models[0]
				case "plaza":
					groups := gjson.Parse(fmt.Sprintf(`[{"id":140,"rate_multiplier":2,"image_rate_independent":true,"image_rate_multiplier":0,"models":[%s]}]`, model.Raw))
					models, err := parseSub2APISiteCatalog(groups, rates)
					require.NoError(t, err)
					require.Len(t, models, 1)
					m = models[0]
				case "public":
					data := gjson.Parse(fmt.Sprintf(`{"groups":[{"id":140,"platform":"gemini"}],"models":[{"group_id":140,"name":%q,"billing_mode":"per_request","price_available":true,"per_request_price":0.05}]}`, name))
					models, err := parsePublicPricingSiteCatalog(data, gjson.Parse("["+group.Raw+"]"), gjson.Parse(`{}`))
					require.NoError(t, err)
					require.Len(t, models, 1)
					m = models[0]
				case "channel":
					var err error
					m, err = siteAuthenticatedChannelModel(group, model, rates)
					require.NoError(t, err)
				}
				require.Empty(t, m.Reason)
				require.Len(t, m.Tiers, 1)
				require.Equal(t, "USD/request", m.Tiers[0].Unit)
				require.InDelta(t, .05, m.Tiers[0].Prices["request"], 1e-12, "keep the upstream per-request multiplier, not its independent image multiplier")
				if name == "text-model" {
					require.False(t, m.Image)
					require.Len(t, siteComparisonTiers(m.Image, m.Tiers), 1)
					return
				}
				require.True(t, m.Image)
				pricing, groups := siteTestPricing()
				groups.group.Platform = PlatformGemini
				groups.group.ImagePrice1K, groups.group.ImagePrice2K, groups.group.ImagePrice4K = nil, nil, nil
				groups.group.ModelPricing = []ChannelModelPricing{{Models: []string{"local-image"}, BillingMode: BillingModeImage, PerRequestPrice: sitePricePtr(.06), Intervals: []PricingInterval{
					{TierLabel: "1K", PerRequestPrice: sitePricePtr(.06)},
					{TierLabel: "2K", PerRequestPrice: sitePricePtr(.04)},
					{TierLabel: "4K", PerRequestPrice: sitePricePtr(.05)},
				}}}
				now := time.Now()
				site := &UpstreamSite{Enabled: true, LastSuccess: &now, Models: []SiteModel{m}}
				binding := &SiteBinding{ID: "binding", GroupID: m.GroupID, Model: name, LocalGroupID: 9, LocalModel: "local-image", Enabled: true}
				account := siteAutomaticAccount(BuildSiteAccountPolicy(site, binding))
				state := pricing.AccountScheduling(context.Background(), account)
				require.Equal(t, "partial", state.Status)
				require.Len(t, state.Tiers, 3)
				ctx := context.WithValue(context.Background(), sitePricingKey{}, pricing)
				for i, tier := range []string{"1K", "2K", "4K"} {
					require.Equal(t, []string{"ready", "exceeded", "equal"}[i], state.Tiers[i].Status)
					veto, reason := SitePriceVeto(WithSiteImageSize(ctx, tier), account)
					require.Equal(t, i != 0, veto)
					require.Equal(t, []string{"", "site_price_exceeded", "site_price_equal"}[i], reason)
				}
				groups.group.AllowEqualPriceScheduling = true
				veto, reason := SitePriceVeto(WithSiteImageSize(ctx, "4K"), account)
				require.False(t, veto)
				require.Empty(t, reason)
			})
		}
	}
}
