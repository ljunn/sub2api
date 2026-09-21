package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamSiteManualPricePersistsAcrossSyncAndUpdatesQueuedRequests(t *testing.T) {
	ctx := context.Background()
	svc, repo, accounts := siteTestService()
	stale := time.Now().Add(-time.Hour)
	site := &UpstreamSite{ID: "manual", Kind: "sub2api", Enabled: true, LastSuccess: &stale,
		Models:   []SiteModel{{GroupID: "2", Model: "image", Image: true, Reason: "missing"}},
		Bindings: []SiteBinding{{ID: "b", GroupID: "2", Model: "image", LocalGroupID: 9, LocalModel: "local-image", AccountID: 31, Enabled: true}}}
	account := siteAutomaticAccount(BuildSiteAccountPolicy(site, &site.Bindings[0]))
	accounts.accounts[31] = account
	require.NoError(t, repo.Save(ctx, site))
	input := SiteManualPriceInput{SiteManualPrice: SiteManualPrice{GroupID: "2", Model: "image", BillingMode: "image", Prices: map[string]float64{"1K": .1, "2K": .2}}}
	updated, err := svc.SaveManualPrice(ctx, site.ID, input)
	require.NoError(t, err)
	p, _ := accounts.accounts[31].SitePolicy()
	require.True(t, p.ManualPrice)
	require.Empty(t, siteTierReason(p, "1K", time.Now()))
	require.Equal(t, "site_price_equal", siteTierReason(p, "2K", time.Now()))
	require.Equal(t, "site_price_unknown", siteTierReason(p, "4K", time.Now()))
	request := WithSiteImageSize(svc.WithPricingContext(ctx), "1K")
	require.NoError(t, CheckSitePriceBeforeSend(request, account, accounts))
	require.True(t, accounts.accounts[31].siteHasEligibleTier(), "manual prices do not expire with automatic price sync")
	ApplySiteManualPrices(updated)
	require.Empty(t, updated.Models[0].Reason)
	require.NotNil(t, updated.Models[0].ManualPrice)
	stored, _ := repo.Get(ctx, site.ID)
	require.Equal(t, "missing", stored.Models[0].Reason, "API projection preserves raw upstream price")
	// A fresh upstream catalogue must not overwrite the chosen cost.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			fmt.Fprint(w, `{"data":{"id":1}}`)
		case "/api/v1/groups/rates":
			fmt.Fprint(w, `{"data":{}}`)
		case "/api/v1/model-plaza":
			fmt.Fprint(w, `{"data":{"groups":[{"id":2,"rate_multiplier":1,"models":[{"name":"image","platform":"openai","pricing":{"billing_mode":"image","per_request_price":9}}]}]}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	stored.BaseURL = server.URL
	require.NoError(t, svc.saveSecret(ctx, stored, &SiteCredentials{AccessToken: "login"}))
	updated, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.Empty(t, updated.Error)
	p, _ = accounts.accounts[31].SitePolicy()
	require.Equal(t, .1, p.Tiers[0].Prices["request"])
	require.NoError(t, CheckSitePriceBeforeSend(request, account, accounts))
	// Editing the manual cost affects even a request holding an older account.
	input.Prices["1K"] = .3
	_, err = svc.SaveManualPrice(ctx, site.ID, input)
	require.NoError(t, err)
	require.Error(t, CheckSitePriceBeforeSend(request, account, accounts))
	input.Automatic = true
	restored, err := svc.SaveManualPrice(ctx, site.ID, input)
	require.NoError(t, err)
	require.Empty(t, restored.ManualPrices)
	p, _ = accounts.accounts[31].SitePolicy()
	require.False(t, p.ManualPrice)
	require.Equal(t, 9.0, p.Tiers[0].Prices["request"])
	require.Error(t, CheckSitePriceBeforeSend(request, account, accounts))
	// A manual price never resurrects a removed model.
	restored.ManualPrices = []SiteManualPrice{input.SiteManualPrice}
	restored.Models = nil
	require.NotEmpty(t, BuildSiteAccountPolicy(restored, &restored.Bindings[0]).Reason)
}

func TestUpstreamSiteManualPriceValidatesUnitsAndMissingValues(t *testing.T) {
	for _, p := range []SiteManualPrice{
		{BillingMode: "image"}, {BillingMode: "image", Prices: map[string]float64{"1K": -1}},
		{BillingMode: "image", Prices: map[string]float64{"1K": math.Inf(1)}},
		{BillingMode: "image", Prices: map[string]float64{"1K": math.NaN()}},
		{BillingMode: "image", Prices: map[string]float64{"request": 1}},
		{BillingMode: "token", Prices: map[string]float64{"input_price": 1}},
	} {
		_, err := p.tiers()
		require.Error(t, err)
	}
	tiers, err := (SiteManualPrice{BillingMode: "image", Prices: map[string]float64{"1K": 0}}).tiers()
	require.NoError(t, err)
	require.Equal(t, 0.0, tiers[0].Prices["request"])
	require.Empty(t, tiers[0].Reason)
	require.NotEmpty(t, tiers[1].Reason)
	tiers, err = (SiteManualPrice{BillingMode: "token", Prices: map[string]float64{"input_price": 2, "output_price": 5, "cache_read_price": 0}}).tiers()
	require.NoError(t, err)
	require.Equal(t, "USD/1M tokens", tiers[0].Unit)
	require.Equal(t, 2.0, tiers[0].Prices["cache_write_price"])
	require.Equal(t, 0.0, tiers[0].Prices["cache_read_price"])
}

func TestUpstreamSiteDiscoveryCreatesAndReusesGroupKey(t *testing.T) {
	creates := 0
	name := ""
	failModels := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			fmt.Fprint(w, `{"data":{"id":1}}`)
		case "/api/v1/model-plaza", "/api/v1/public/model-pricing":
			w.WriteHeader(404)
		case "/api/v1/groups/available":
			fmt.Fprint(w, `{"data":[{"id":17,"name":"Banana","platform":"gemini","status":"active","rate_multiplier":1}]}`)
		case "/api/v1/groups/rates":
			fmt.Fprint(w, `{"data":{}}`)
		case "/api/v1/channels/available":
			fmt.Fprint(w, `{"data":[]}`)
		case "/api/v1/keys":
			if r.Method == http.MethodPost {
				creates++
				var input struct {
					Name    string `json:"name"`
					GroupID int64  `json:"group_id"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
				require.EqualValues(t, 17, input.GroupID)
				name = input.Name
				fmt.Fprint(w, `{"data":{"key":"private-generated-key"}}`)
			} else if name == "" {
				fmt.Fprint(w, `{"data":{"items":[]}}`)
			} else {
				writeSiteJSON(w, map[string]any{"data": map[string]any{"items": []any{map[string]any{"name": name, "group_id": 17, "status": "active", "key": "private-generated-key"}}}})
			}
		case "/v1/models":
			require.Equal(t, "Bearer private-generated-key", r.Header.Get("Authorization"))
			if failModels {
				w.WriteHeader(503)
				return
			}
			fmt.Fprint(w, `{"data":[{"id":"gemini-image"}]}`)
		default:
			t.Errorf("unexpected endpoint %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	svc, repo, _ := siteTestService()
	ctx := context.Background()
	site, err := svc.Save(ctx, "", SiteInput{Name: "Auto key", BaseURL: server.URL, Kind: "sub2api", AuthMode: "token", AccessToken: "login", Enabled: true})
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		site, err = svc.Sync(ctx, site.ID)
		require.NoError(t, err)
		require.Empty(t, site.Error)
		require.Len(t, site.Models, 1)
	}
	require.Equal(t, 1, creates)
	saved, _ := repo.Get(ctx, site.ID)
	credentials, err := svc.credentials(saved)
	require.NoError(t, err)
	require.Len(t, credentials.Keys, 1, "new discovery keys are persisted encrypted")
	raw, err := json.Marshal(site)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "private-generated-key")
	// Lost local key state recovers the existing upstream key instead of duplicating it.
	credentials.Keys = nil
	require.NoError(t, svc.saveSecret(ctx, saved, credentials))
	_, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.Equal(t, 1, creates)
	failModels = true
	site, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.Greater(t, len(site.Warnings), 1)
	require.Equal(t, 1, creates, "upstream model errors must not cause key churn")
}
