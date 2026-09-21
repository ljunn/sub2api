package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestUpstreamSiteAuthenticatedCatalogKeepsModelsAndReportsPartialGroups(t *testing.T) {
	plazaEnabled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			fmt.Fprint(w, `{"data":{"id":1}}`)
		case "/api/v1/model-plaza":
			if plazaEnabled {
				fmt.Fprint(w, `{"data":{"groups":[]}}`)
				return
			}
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"Model plaza is not enabled"}`)
		case "/api/v1/public/model-pricing":
			w.WriteHeader(404)
		case "/api/v1/groups/rates":
			fmt.Fprint(w, `{"data":{}}`)
		case "/api/v1/groups/available":
			fmt.Fprint(w, `{"data":[
			 {"id":15,"name":"Images","platform":"openai","rate_multiplier":0.008,"allow_image_generation":true,"image_price_1k":1,"image_price_2k":null,"image_price_4k":1},
			 {"id":17,"name":"No key","platform":"gemini","rate_multiplier":1},
			 {"id":23,"name":"Other images","platform":"openai","rate_multiplier":1,"allow_image_generation":true},
			 {"id":24,"status":"inactive"}]}`)
		case "/api/v1/channels/available":
			fmt.Fprint(w, `{"data":[]}`)
		case "/api/v1/keys":
			if r.Method == http.MethodPost {
				w.WriteHeader(403)
				return
			}
			if r.URL.Query().Get("search") != "" {
				fmt.Fprint(w, `{"data":{"items":[]}}`)
				return
			}
			id := r.URL.Query().Get("group_id")
			if id == "17" {
				fmt.Fprint(w, `{"data":{"items":[]}}`)
			} else {
				require.Contains(t, []string{"15", "23"}, id)
				fmt.Fprintf(w, `{"data":{"items":[{"group_id":%s,"status":"active","key":"key-%s"}]}}`, id, id)
			}
		case "/v1/models":
			require.Contains(t, []string{"Bearer key-15", "Bearer key-23"}, r.Header.Get("Authorization"))
			fmt.Fprint(w, `{"data":[{"id":"gpt-image-2"},{"id":"text-model"}]}`)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	svc, _, accounts := siteTestService()
	site, err := svc.Save(context.Background(), "", SiteInput{Name: "No plaza", BaseURL: server.URL, Kind: "sub2api", AuthMode: "token", AccessToken: "test-access", Enabled: true})
	require.NoError(t, err)
	site, err = svc.Sync(context.Background(), site.ID)
	require.NoError(t, err)
	assert.Equal(t, "connected", site.Status)
	assert.Empty(t, site.Error)
	require.Len(t, site.Models, 4)
	require.Len(t, site.Warnings, 2)
	assert.Contains(t, site.Warnings[1], "No key")
	assert.NotContains(t, strings.Join(site.Warnings, " "), "key-15")
	assert.Zero(t, accounts.creates)
	assert.Empty(t, site.Bindings)
	m := site.Models[0]
	require.Len(t, m.Tiers, 3)
	assert.Equal(t, .008, m.Tiers[0].Prices["request"])
	assert.Empty(t, m.Tiers[1].Prices, "null price must never become zero")
	assert.Contains(t, m.Tiers[0].Note, "参考价")
	assert.NotEmpty(t, m.Reason, "image capability does not prove per-image billing")
	policy := BuildSiteAccountPolicy(site, &SiteBinding{GroupID: m.GroupID, Model: m.Model, Enabled: true})
	assert.Equal(t, "site_price_unknown", siteTierReason(policy, "1K", time.Now()))
	assert.Empty(t, site.Models[1].Tiers, "group image prices must not price a text model")
	plazaEnabled = true
	site, err = svc.Sync(context.Background(), site.ID)
	require.NoError(t, err)
	assert.Empty(t, site.Warnings, "a recovered complete catalog clears partial sync warnings")
}

func TestUpstreamSiteAuthenticatedChannelPricesUseActualMultipliers(t *testing.T) {
	group := gjson.Parse(`{"id":3,"name":"Images","platform":"openai","rate_multiplier":2,"peak_rate_enabled":true,"peak_rate_multiplier":3,"image_price_1k":0.1}`)
	model := gjson.Parse(`{"name":"gpt-image-2","platform":"openai","pricing":{"billing_mode":"image","per_request_price":0.2,"intervals":[{"tier_label":"4K","per_request_price":0.4}]}}`)
	prices, err := siteAuthenticatedChannelModel(group, model, gjson.Parse(`{"3":0.5}`))
	require.NoError(t, err)
	assert.Empty(t, prices.Reason)
	require.Len(t, prices.Tiers, 3)
	for i, want := range []float64{.15, .3, .6} {
		assert.InDelta(t, want, prices.Tiers[i].Prices["request"], 1e-12)
	}
	group = gjson.Parse(`{"id":3,"platform":"openai","rate_multiplier":2,"image_rate_independent":true,"image_rate_multiplier":0.25}`)
	prices, err = siteAuthenticatedChannelModel(group, model, gjson.Parse(`{"3":0.5}`))
	require.NoError(t, err)
	assert.InDelta(t, .05, prices.Tiers[0].Prices["request"], 1e-12)
	group = gjson.Parse(`{"id":3,"platform":"openai","rate_multiplier":0}`)
	prices, err = siteAuthenticatedChannelModel(group, model, gjson.Parse(`{}`))
	require.NoError(t, err)
	assert.Contains(t, prices.Reason, "隐藏价格")
}

func TestUpstreamSiteAuthenticatedChannelCatalogWithoutKeysAndPrivateGroups(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		switch r.URL.Path {
		case "/api/v1/groups/available":
			fmt.Fprint(w, `{"data":[{"id":3,"platform":"openai","name":"Private","rate_multiplier":2}]}`)
		case "/api/v1/groups/rates":
			fmt.Fprint(w, `{"data":{"3":0.5}}`)
		case "/api/v1/channels/available":
			fmt.Fprint(w, `{"data":[{"platforms":[
			 {"groups":[{"id":3}],"supported_models":[{"name":"text","platform":"openai","pricing":{"billing_mode":"token","input_price":0.000001,"output_price":0.000003}}]},
			 {"groups":[{"id":99}],"supported_models":[{"name":"unauthorized","platform":"openai"}]}
			]},{"platforms":[{"groups":[{"id":3}],"supported_models":[{"name":"text","platform":"openai","pricing":{"billing_mode":"token","input_price":0.000002,"output_price":0.000004}}]}]}]}`)
		case "/api/v1/keys":
			require.Equal(t, "3", r.URL.Query().Get("group_id"))
			fmt.Fprint(w, `{"data":{"items":[]}}`)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	a := newSiteAdapter(&UpstreamSite{BaseURL: server.URL}, &SiteCredentials{})
	models, err := a.authenticatedPricingCatalog(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 1)
	assert.Equal(t, "3", models[0].GroupID)
	assert.Empty(t, models[0].Reason)
	assert.Equal(t, map[string]float64{"input_price": 1, "output_price": 2}, models[0].Tiers[0].Prices)
	assert.Len(t, a.warnings, 1)
}

func TestUpstreamSiteAuthenticatedFallbackDoesNotHidePricingEndpointFailures(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/public/model-pricing":
					w.WriteHeader(status)
				default:
					t.Errorf("must not ignore endpoint failure: %s", r.URL.Path)
				}
			}))
			defer server.Close()
			a := newSiteAdapter(&UpstreamSite{BaseURL: server.URL}, &SiteCredentials{})
			_, err := a.publicPricingCatalog(context.Background())
			require.ErrorContains(t, err, fmt.Sprint(status))
		})
	}
}

func TestUpstreamSiteAuthenticatedMultipleChannelsCannotHideUnknownCosts(t *testing.T) {
	group := gjson.Parse(`{"id":3,"platform":"openai","rate_multiplier":1}`)
	known, err := siteAuthenticatedChannelModel(group, gjson.Parse(`{"name":"m","pricing":{"billing_mode":"per_request","per_request_price":0.01}}`), gjson.Parse(`{}`))
	require.NoError(t, err)
	unknown, err := siteAuthenticatedChannelModel(group, gjson.Parse(`{"name":"m","pricing":null}`), gjson.Parse(`{}`))
	require.NoError(t, err)
	assert.NotEmpty(t, siteMergeChannelPrice(known, unknown).Reason)
	assert.NotEmpty(t, siteMergeChannelPrice(unknown, known).Reason)
}
