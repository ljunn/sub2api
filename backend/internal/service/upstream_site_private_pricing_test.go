package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const sitePrivateImageGroup = `{"id":19,"name":"GPT生图对接组","description":"旧说明 0.03/张","platform":"openai","status":"active","is_exclusive":true,"public_pricing_visible":false,"allow_image_generation":true,"rate_multiplier":0,"image_rate_multiplier":0,"image_price_1k":0.04,"image_price_2k":0.04,"image_price_4k":0.04}`

func TestUpstreamSitePrivateGroupUsesExistingMatchingKeyAndOwnPrices(t *testing.T) {
	modelReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method, "discovery must not create keys")
		switch r.URL.Path {
		case "/api/v1/keys":
			assert.Equal(t, "Bearer login-token", r.Header.Get("Authorization"))
			assert.Equal(t, "19", r.URL.Query().Get("group_id"))
			fmt.Fprint(w, `{"code":0,"data":{"items":[{"group_id":4,"status":"active","key":"wrong-group"},{"group_id":19,"status":"inactive","key":"inactive"},{"group_id":19,"status":"active","key":"sk-***"},{"group_id":19,"status":"active","key":"expired"},{"group_id":19,"status":"active","key":"private-key"}],"total":5}}`)
		case "/v1/models":
			modelReads++
			if r.Header.Get("Authorization") == "Bearer expired" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			require.Equal(t, "Bearer private-key", r.Header.Get("Authorization"))
			fmt.Fprint(w, `{"data":[{"id":"gpt-image-2"},{"id":"unknown-text"}]}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	credentials := &SiteCredentials{AccessToken: "login-token"}
	adapter := newSiteAdapter(&UpstreamSite{BaseURL: server.URL, Kind: "sub2api"}, credentials)
	available := gjson.Parse(`[{"id":4,"status":"active"},` + sitePrivateImageGroup + `]`)
	models, err := adapter.privatePricingCatalog(context.Background(), gjson.Parse(sitePublicPrices), available, gjson.Parse(`{}`))
	require.NoError(t, err)
	require.Len(t, models, 2)
	assert.Equal(t, 2, modelReads)
	assert.Equal(t, "19", models[0].GroupID)
	assert.Equal(t, "GPT生图对接组", models[0].GroupName)
	require.Len(t, models[0].Tiers, 3)
	for _, tier := range models[0].Tiers {
		assert.Equal(t, .04, tier.Prices["request"], "use this group's numeric prices, not descriptions or another group's prices")
	}
	assert.Empty(t, models[1].Tiers)
	assert.Contains(t, models[1].Reason, "完整计费单价")
	assert.Equal(t, "login-token", credentials.AccessToken)
	assert.Empty(t, credentials.Keys)
	raw, err := json.Marshal(models)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "private-key")
}

func TestUpstreamSitePrivateGroupCannotSilentlyDisappearWithoutAKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		require.Equal(t, "/api/v1/keys", r.URL.Path)
		fmt.Fprint(w, `{"code":0,"data":{"items":[],"total":0}}`)
	}))
	defer server.Close()
	adapter := newSiteAdapter(&UpstreamSite{BaseURL: server.URL}, &SiteCredentials{})
	_, err := adapter.privatePricingCatalog(context.Background(), gjson.Parse(sitePublicPrices), gjson.Parse(`[`+sitePrivateImageGroup+`]`), gjson.Parse(`{}`))
	require.ErrorContains(t, err, "GPT生图对接组")
	require.ErrorContains(t, err, "有效 API Key")
}

func TestUpstreamSiteDisabledModelPlazaStillReadsPublicPrices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodGet, r.Method)
		switch r.URL.Path {
		case "/api/v1/auth/me":
			fmt.Fprint(w, `{"code":0,"data":{"id":1}}`)
		case "/api/v1/model-plaza":
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"code":404,"message":"Model plaza is not enabled"}`)
		case "/api/v1/public/model-pricing":
			fmt.Fprintf(w, `{"code":0,"data":%s}`, sitePublicPrices)
		case "/api/v1/groups/available":
			fmt.Fprintf(w, `{"code":0,"data":%s}`, sitePublicGroups)
		case "/api/v1/groups/rates":
			fmt.Fprint(w, `{"code":0,"data":{}}`)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	adapter := newSiteAdapter(&UpstreamSite{BaseURL: server.URL, Kind: "sub2api"}, &SiteCredentials{AccessToken: "valid-login"})
	models, err := adapter.catalog(context.Background())
	require.NoError(t, err)
	require.Len(t, models, 3)
	assert.Equal(t, .035, models[0].Tiers[0].Prices["request"])
}
