//go:build unit

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func longXiaSiteCatalogue(t *testing.T) []SiteModel {
	t.Helper()
	models, err := parseNewAPISiteCatalog(gjson.Parse(`{"data":[
	{"model_name":"LongXia-video-seedance2_5-standard-480p-express-PerSecond","quota_type":1,"model_price":0.36,"enable_groups":["default"],"supported_endpoint_types":["openai-video"]},
	{"model_name":"LongXia-video-seedance2_5-standard-720p-express-PerSecond","quota_type":1,"model_price":0.5,"enable_groups":["default"],"supported_endpoint_types":["openai-video"]}
	],"group_ratio":{"default":1},"usable_group":{"default":"Default"}}`))
	require.NoError(t, err)
	return models
}

func TestLongXiaSiteCatalogUnitsAndUnknownSKUs(t *testing.T) {
	models := longXiaSiteCatalogue(t)
	for i, model := range models {
		require.Empty(t, model.Reason)
		require.NotNil(t, model.LongXia)
		require.Equal(t, int64(4), model.LongXia.DurationMin)
		require.Equal(t, int64(25), model.LongXia.DurationMax)
		require.Equal(t, []string{"480p", "720p"}[i], model.Tiers[0].Key)
		require.Equal(t, "USD/second", model.Tiers[0].Unit)
		require.Equal(t, []float64{.36, .5}[i], model.Tiers[0].Prices["second"])
	}
	for _, tc := range []struct {
		name, row string
		blocked   bool
		unit      string
	}{
		{strings.TrimSuffix(longXiaTestModel, "-PerSecond"), `{"quota_type":1,"model_price":10,"supported_endpoint_types":["openai-video"]}`, false, "USD/request"},
		{"LongXia-video-new-PerSecond", `{"quota_type":1,"model_price":1,"supported_endpoint_types":["openai-video"]}`, true, ""},
		{longXiaTestModel, `{"quota_type":1,"model_price":0.5}`, true, ""},
		{longXiaTestModel, `{"quota_type":0,"model_price":0.5,"supported_endpoint_types":["openai-video"]}`, true, ""},
	} {
		m := SiteModel{Model: tc.name}
		require.True(t, parseLongXiaSiteModel(&m, gjson.Parse(tc.row), 2))
		require.Equal(t, tc.blocked, m.Reason != "")
		if !tc.blocked {
			require.Equal(t, tc.unit, m.Tiers[0].Unit)
			require.Equal(t, 20.0, m.Tiers[0].Prices["request"])
		}
	}
}

func TestLongXiaSiteSameAliasRoutesByResolutionAndVideoPrice(t *testing.T) {
	pricing, groups := siteTestPricing()
	groups.group.ModelPricing = []ChannelModelPricing{{Models: []string{"seedance-2.5"}, BillingMode: BillingModeVideo, PerRequestPrice: sitePricePtr(1), Intervals: []PricingInterval{{TierLabel: "480p", PerRequestPrice: sitePricePtr(.6)}, {TierLabel: "720p", PerRequestPrice: sitePricePtr(.8)}}}}
	now := time.Now()
	site := &UpstreamSite{ID: "site", Kind: "newapi", Enabled: true, LastSuccess: &now, Models: longXiaSiteCatalogue(t)}
	for _, model := range site.Models {
		p := BuildSiteAccountPolicy(site, &SiteBinding{ID: "binding", Enabled: true, GroupID: "default", Model: model.Model, LocalModel: "seedance-2.5", LocalGroupID: 9})
		got := pricing.apply(context.Background(), p, false)
		require.Empty(t, got.Limits[0].Reason)
		want := .8
		if model.LongXia.Resolution == "480p" {
			want = .6
		}
		require.InDelta(t, want, got.Limits[0].Selling["second"], 1e-10)
		a := siteAutomaticAccount(got)
		ctx := context.WithValue(context.Background(), sitePricingKey{}, pricing)
		for _, res := range []string{"480p", "720p", "1080p"} {
			request := WithSitePriceRequest(ctx, []byte(fmt.Sprintf(`{"model":"seedance-2.5","content":[{"type":"text","text":"waves"}],"resolution":%q,"duration":4}`, res)))
			veto, _ := SitePriceVeto(request, a)
			require.Equal(t, res != model.LongXia.Resolution, veto, res)
		}
		for _, duration := range []string{"3", "26", "4.5", `"4"`, "null"} {
			veto, _ := SitePriceVeto(WithSitePriceRequest(ctx, []byte(`{"content":[],"duration":`+duration+`}`)), a)
			require.True(t, veto)
		}
		veto, _ := SitePriceVeto(WithSitePriceRequest(ctx, []byte(`{"content":[]}`)), a)
		require.False(t, veto, "omitted resolution/duration use the selected SKU's defaults")
		groups.group.ModelPricing[0].BillingMode = BillingModeToken
		veto, _ = SitePriceVeto(WithSitePriceRequest(ctx, []byte(`{"content":[]}`)), a)
		require.True(t, veto, "a token price cannot cover LongXia seconds")
		groups.group.ModelPricing[0].BillingMode = BillingModeVideo
	}
}

func TestLongXiaSiteBindingsEnableProtocolAndPreserveAcceptedTask(t *testing.T) {
	svc, repo, accounts := siteTestService()
	svc.preview = false
	now := time.Now()
	models := longXiaSiteCatalogue(t)
	site := &UpstreamSite{ID: "site", Name: "LongXia", BaseURL: "https://api8.longxiaai.store", Kind: "newapi", Enabled: true, LastSuccess: &now, Models: models}
	keys := map[string]string{}
	for i, m := range models {
		id := fmt.Sprint(i)
		site.Bindings = append(site.Bindings, SiteBinding{ID: id, GroupID: "default", Model: m.Model, LocalGroupID: 9, LocalModel: "video", Enabled: true})
		keys[id] = "test-key"
	}
	require.NoError(t, svc.saveSecret(context.Background(), site, &SiteCredentials{Keys: keys}))
	for _, b := range site.Bindings {
		bound, err := svc.Bind(context.Background(), site.ID, b)
		require.NoError(t, err)
		_ = bound
	}
	require.Len(t, accounts.accounts, 2)
	for _, a := range accounts.accounts {
		require.True(t, a.IsLongXia())
		require.Equal(t, []int64{9}, a.GroupIDs)
		p, _ := a.SitePolicy()
		require.NotNil(t, p.LongXia)
		p.Enabled = false
		p.FreshUntil = time.Now().Add(-time.Hour)
		raw, _ := json.Marshal(p)
		var extra map[string]any
		require.NoError(t, json.Unmarshal(raw, &extra))
		a.Extra[SitePolicyExtraKey] = extra
		upstream := &grokMediaContentUpstreamStub{response: grokMediaContentStatusResponse(`{"id":"task_1","status":"in_progress"}`)}
		gateway := &OpenAIGatewayService{accountRepo: accounts, httpUpstream: upstream, resolver: longXiaTestPricing(BillingModeVideo)}
		groupID := int64(9)
		selection, _, err := gateway.SelectMediaVideoRequestAccount(context.Background(), &groupID, "verified-owner", a.ID, "video", PlatformOpenAI)
		require.NoError(t, err)
		ctx := ContextWithSelectionProfitGate(context.Background(), selection)
		_, veto, _ := gateway.ProfitControlVetoLatest(ctx, a)
		require.False(t, veto)
		c, _ := grokMediaContentTestContext(http.MethodGet, "/", nil)
		_, err = gateway.ForwardSeedance(ctx, c, a, SeedanceEndpointStatus, "seedance:longxia:task_1", nil)
		require.NoError(t, err)
		upstream.request = nil
		c, _ = grokMediaContentTestContext(http.MethodPost, "/", nil)
		_, err = gateway.ForwardSeedance(context.Background(), c, a, SeedanceEndpointCreate, "", []byte(`{"model":"video","content":[{"type":"text","text":"waves"}],"duration":4}`))
		require.Error(t, err)
		require.Nil(t, upstream.request, "paused creation never reaches the upstream")
		wrongGroup := int64(88)
		_, _, err = gateway.SelectMediaVideoRequestAccount(ctx, &wrongGroup, "verified-owner", a.ID, "video", PlatformOpenAI)
		require.Error(t, err)
	}
	_, err := svc.SaveManualPrice(context.Background(), "site", SiteManualPriceInput{SiteManualPrice: SiteManualPrice{GroupID: "default", Model: models[0].Model, BillingMode: "per_request", Prices: map[string]float64{"request": 1}}})
	require.Error(t, err)
	require.Empty(t, repo.sites["site"].ManualPrices)
}
