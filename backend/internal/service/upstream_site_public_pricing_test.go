package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const sitePublicPrices = `{"groups":[{"id":4,"name":"GPT原生图片","platform":"openai"},{"id":7,"name":"restricted"}],"models":[
 {"group_id":4,"name":"gpt-image-2","billing_mode":"image","price_available":true,"image_price_1k":0.035,"image_price_2k":0.07,"image_price_4k":0.08},
 {"group_id":4,"name":"text-model","billing_mode":"token","price_available":true,"input_price":0.0000009,"output_price":0.0000054,"cache_read_price":0.00000009},
 {"group_id":4,"name":"video-model","billing_mode":"per_request","price_available":true,"per_request_price":1.3},
 {"group_id":7,"name":"restricted-model","billing_mode":"image","price_available":true,"image_price_1k":0.01}
],"recharge_multiplier":100}`
const sitePublicGroups = `[{"id":4,"status":"active","rate_multiplier":0,"image_rate_multiplier":0,"image_rate_independent":true}]`

func TestUpstreamSitePublicPricingFallbackAndNoAutomaticBinding(t *testing.T) {
	for _, status := range []int{404, 401, 403, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			priceReads := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, "Bearer test-access", r.Header.Get("Authorization"))
				switch r.URL.Path {
				case "/api/v1/auth/me":
					fmt.Fprint(w, `{"code":0,"data":{"id":1}}`)
				case "/api/v1/model-plaza":
					w.WriteHeader(status)
				case "/api/v1/public/model-pricing":
					priceReads++
					fmt.Fprintf(w, `{"code":0,"data":%s}`, sitePublicPrices)
				case "/api/v1/groups/available":
					fmt.Fprintf(w, `{"code":0,"data":%s}`, sitePublicGroups)
				case "/api/v1/groups/rates":
					fmt.Fprint(w, `{"code":0,"data":{}}`)
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			svc, _, accounts := siteTestService()
			site, err := svc.Save(context.Background(), "", SiteInput{Name: "飞羽", BaseURL: server.URL, Kind: "sub2api", AuthMode: "token", AccessToken: "test-access", Enabled: true})
			require.NoError(t, err)
			result, err := svc.Sync(context.Background(), site.ID)
			require.NoError(t, err)
			if status != 404 {
				assert.Contains(t, result.Error, fmt.Sprint(status))
				assert.Zero(t, priceReads)
				return
			}
			require.Empty(t, result.Error)
			require.Len(t, result.Models, 3)
			assert.Equal(t, 1, priceReads)
			assert.Empty(t, result.Bindings)
			assert.Zero(t, accounts.creates)
		})
	}
}

func TestUpstreamSitePublicFinalPricesDoNotApplyHiddenRatesOrRechargeConversion(t *testing.T) {
	models, err := parsePublicPricingSiteCatalog(gjson.Parse(sitePublicPrices), gjson.Parse(sitePublicGroups), gjson.Parse(`{}`))
	require.NoError(t, err)
	require.Len(t, models, 3)
	assert.Equal(t, "GPT原生图片", models[0].GroupName)
	assert.Equal(t, "openai", models[0].Platform)
	require.Len(t, models[0].Tiers, 3)
	for i, want := range []float64{0.035, 0.07, 0.08} {
		assert.InDelta(t, want, models[0].Tiers[i].Prices["request"], 1e-12)
	}
	assert.Equal(t, "USD/1M tokens", models[1].Tiers[0].Unit)
	assert.InDelta(t, .9, models[1].Tiers[0].Prices["input_price"], 1e-12)
	assert.InDelta(t, 5.4, models[1].Tiers[0].Prices["output_price"], 1e-12)
	assert.InDelta(t, .09, models[1].Tiers[0].Prices["cache_read_price"], 1e-12)
	assert.Equal(t, "USD/request", models[2].Tiers[0].Unit)
	assert.Equal(t, 1.3, models[2].Tiers[0].Prices["request"])
}

func TestUpstreamSitePublicPricingUnknownPricesStayBlocked(t *testing.T) {
	for _, tc := range []struct{ name, model, reason string }{
		{"unpublished", `"billing_mode":"token","price_available":false`, "尚未公布"},
		{"missing output", `"billing_mode":"token","price_available":true,"input_price":0`, "缺少输出"},
		{"unknown billing", `"billing_mode":"video","price_available":true`, "计费方式"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := gjson.Parse(fmt.Sprintf(`{"groups":[{"id":4}],"models":[{"group_id":4,"name":"model",%s}]}`, tc.model))
			models, err := parsePublicPricingSiteCatalog(data, gjson.Parse(sitePublicGroups), gjson.Parse(`{}`))
			require.NoError(t, err)
			require.Len(t, models, 1)
			assert.Contains(t, models[0].Reason, tc.reason)
		})
	}
	data := gjson.Parse(`{"groups":[{"id":4}],"models":[{"group_id":4,"name":"partial-image","billing_mode":"image","price_available":true,"image_price_1k":0,"image_price_2k":null,"image_price_4k":"0"}]}`)
	models, err := parsePublicPricingSiteCatalog(data, gjson.Parse(sitePublicGroups), gjson.Parse(`{}`))
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, map[string]float64{"request": 0}, models[0].Tiers[0].Prices)
	assert.Empty(t, models[0].Tiers[0].Reason)
	for _, tier := range models[0].Tiers[1:] {
		assert.Empty(t, tier.Prices)
		assert.NotEmpty(t, tier.Reason)
	}
	models, err = parsePublicPricingSiteCatalog(gjson.Parse(sitePublicPrices), gjson.Parse(sitePublicGroups), gjson.Parse(`{"4":0.5}`))
	require.NoError(t, err)
	for _, model := range models {
		assert.Contains(t, model.Reason, "个人专属费率")
		assert.Empty(t, model.Tiers)
	}
	_, err = parsePublicPricingSiteCatalog(gjson.Parse(sitePublicPrices), gjson.Parse(sitePublicGroups), gjson.Parse(`null`))
	require.Error(t, err)
}

func TestUpstreamSitePublicContextTiersUseHighestFinalCost(t *testing.T) {
	data := gjson.Parse(`{"groups":[{"id":4}],"models":[{"group_id":4,"name":"text-model","billing_mode":"token","price_available":true,"input_price":0.000001,"output_price":0.000002,"tiers":[{"input_price":0.000003,"output_price":0.000004,"cache_read_price":0.0000005}]}]}`)
	models, err := parsePublicPricingSiteCatalog(data, gjson.Parse(sitePublicGroups), gjson.Parse(`{}`))
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Empty(t, models[0].Reason)
	assert.Equal(t, map[string]float64{"input_price": 3, "output_price": 4, "cache_read_price": .5}, models[0].Tiers[0].Prices)
}
