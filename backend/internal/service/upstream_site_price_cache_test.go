package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamSitePriceCacheSurvivesFailedRefreshAndRecovery(t *testing.T) {
	ctx := context.Background()
	mode := "known"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"id":1}}`)
		case "/api/pricing":
			if mode == "failure" {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			model := `{"model_name":"gemini-image","enable_groups":["g"],"supported_endpoint_types":["image-generation"],"quota_type":1,"model_price":0.05}`
			if mode == "unknown" {
				model = `{"model_name":"gemini-image","enable_groups":["g"],"supported_endpoint_types":["image-generation"],"billing_expr":"unrecognized(cost)"}`
			} else if mode == "higher" {
				model = `{"model_name":"gemini-image","enable_groups":["g"],"supported_endpoint_types":["image-generation"],"quota_type":1,"model_price":0.9}`
			} else if mode == "removed" {
				model = ""
			}
			fmt.Fprintf(w, `{"success":true,"group_ratio":{"g":1},"usable_group":{"g":"Default"},"data":[%s]}`, model)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	svc, repo, accounts := siteTestService()
	svc.preview = false
	ctx = svc.WithPricingContext(ctx)
	site, err := svc.Save(ctx, "", SiteInput{Name: "cached", Kind: "newapi", BaseURL: server.URL, AuthMode: "token", AccessToken: "test-access", UserID: 1, Enabled: true})
	require.NoError(t, err)
	site, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.Empty(t, site.Error)
	require.Len(t, site.Models, 1)
	knownAt := time.Now().UTC().Add(-24 * time.Hour)
	site.Models[0].PriceUpdatedAt = &knownAt
	site.LastSuccess = &knownAt
	b := SiteBinding{ID: "cached-binding", GroupID: "g", Model: "gemini-image", LocalGroupID: 9, LocalModel: "local-image", Enabled: true,
		Limits: []SiteTierLimit{{Key: "1K", Enabled: false}, {Key: "2K", Enabled: true}, {Key: "4K", Enabled: false}}}
	creds, err := svc.credentials(site)
	require.NoError(t, err)
	creds.Keys[b.ID] = "existing-inference-key"
	site.Bindings = append(site.Bindings, b)
	require.NoError(t, svc.saveSecret(ctx, site, creds))
	site, err = svc.Bind(ctx, site.ID, b)
	require.NoError(t, err)
	accountID := site.Bindings[0].AccountID
	queued, err := accounts.GetByID(ctx, accountID)
	require.NoError(t, err)
	for _, nextMode := range []string{"unknown", "unknown", "failure"} {
		mode = nextMode
		// Force an old persisted snapshot, then simulate a restarted service.
		past := time.Now().Add(-24 * time.Hour)
		repo.sites[site.ID].LastSuccess = &past
		svc = NewUpstreamSiteService(repo, accounts, siteTestAdmin{}, siteTestCipher{})
		svc.pricing, _ = siteTestPricing()
		ctx = svc.WithPricingContext(context.Background())
		site, err = svc.Sync(ctx, site.ID)
		require.NoError(t, err)
		require.Equal(t, knownAt, *site.Models[0].PriceUpdatedAt)
		require.Equal(t, .05, site.Models[0].Tiers[0].Prices["request"])
		require.Empty(t, site.History, "refresh failures are not price changes")
		require.NotEmpty(t, site.Warnings)
		account := accounts.accounts[accountID]
		require.True(t, account.siteHasEligibleTier())
		require.NoError(t, CheckSitePriceBeforeSend(WithSiteImageSize(ctx, "2K"), queued, accounts))
		p, _ := account.SitePolicy()
		require.Empty(t, siteTierReason(p, "2K", time.Now().Add(72*time.Hour)))
		require.Equal(t, "site_tier_disabled", siteTierReason(p, "1K", time.Now()))
		require.Equal(t, "partial", svc.pricing.AccountScheduling(ctx, account).Status)
	}
	// A newly published higher price takes effect even for already queued requests.
	mode = "higher"
	site, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.Empty(t, site.Warnings)
	require.Empty(t, site.Models[0].PriceSyncError)
	require.True(t, site.Models[0].PriceUpdatedAt.After(knownAt))
	require.Len(t, site.History, 1)
	require.Error(t, CheckSitePriceBeforeSend(WithSiteImageSize(ctx, "2K"), queued, accounts))
	mode = "known"
	site, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.NoError(t, CheckSitePriceBeforeSend(WithSiteImageSize(ctx, "2K"), queued, accounts))
	mode = "removed"
	site, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.Empty(t, site.Models)
	require.False(t, accounts.accounts[accountID].siteHasEligibleTier())
	require.Error(t, CheckSitePriceBeforeSend(WithSiteImageSize(ctx, "2K"), queued, accounts))
}

func TestUpstreamSitePriceCacheKeepsPartialGroupsAndKnownTierUpdates(t *testing.T) {
	before := time.Now().Add(-time.Hour)
	now := time.Now()
	prior := SiteModel{GroupID: "g", Model: "image", Image: true, Platform: PlatformOpenAI, Tiers: []SitePriceTier{
		{Key: "1K", Unit: "USD/image", Prices: map[string]float64{"request": .04}},
		{Key: "2K", Unit: "USD/image", Prices: map[string]float64{"request": .05}},
	}}
	site := &UpstreamSite{LastSuccess: &before, Models: []SiteModel{prior}}
	raw, err := json.Marshal(prior)
	require.NoError(t, err)
	var next SiteModel
	require.NoError(t, json.Unmarshal(raw, &next))
	next.Tiers[0].Prices["request"] = .07
	next.Tiers[1].Prices = nil
	models, warnings := mergeSitePriceCache(site, []SiteModel{next}, nil, now)
	require.Len(t, warnings, 1)
	require.Equal(t, .07, models[0].Tiers[0].Prices["request"], "known price increase must not be lost when another tier is missing")
	require.Equal(t, .05, models[0].Tiers[1].Prices["request"])
	require.Equal(t, before, *models[0].PriceUpdatedAt)
	models, warnings = mergeSitePriceCache(site, nil, map[string]string{"g": "HTTP 503"}, now)
	require.Len(t, models, 1)
	require.Len(t, warnings, 1)
	require.Equal(t, prior.Tiers, models[0].Tiers)
	models, warnings = mergeSitePriceCache(site, nil, nil, now)
	require.Empty(t, models, "a successfully removed group is not a failed lookup")
	require.Empty(t, warnings)
	next.GroupID = "other"
	next.Tiers[1].Prices = nil
	models, _ = mergeSitePriceCache(site, []SiteModel{next}, nil, now)
	require.Empty(t, models[0].Tiers[1].Prices, "prices cannot leak across groups")
	// An unverified reference price must not become a cached purchase price.
	site.Models[0].Reason = "reference price only"
	next.GroupID = "g"
	models, _ = mergeSitePriceCache(site, []SiteModel{next}, nil, now)
	require.Empty(t, models[0].Tiers[1].Prices)
}
