package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const wuzuTestModel = `{"config_key":"gpt-image-2-web","id":"gpt-image-2","mode":"image","enabled":true,"disable_api_key":false,"supports_edit":true,"supports_upscale":true,"size_map":{"16:9":"1536x864"},"quota_cost_mode":"tier","quota_cost_tiers":{"1k":0.1,"2k":0.2,"4k":0.3},"allowed_user_types":[]}`

func TestWuzuSiteLifecycle(t *testing.T) {
	ctx := context.Background()
	logins, creates := 0, 0
	createdName := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/auth/password-login":
			logins++
			require.Empty(t, r.Header.Get("Authorization"))
			var input map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
			require.Equal(t, map[string]string{"username": "member", "password": "console-password"}, input)
			fmt.Fprint(w, `{"ok":true,"key":"console-key","subject_id":"u-abc123"}`)
		case "/api/auth/me":
			if r.Header.Get("Authorization") != "Bearer console-key" {
				w.WriteHeader(401)
				return
			}
			fmt.Fprint(w, `{"identity":{"id":"u-abc123","enabled":true,"remaining":1234.5,"unlimited":false,"effective_image_concurrency":2000}}`)
		case "/api/me/site-api-keys/capability":
			require.Equal(t, "Bearer console-key", r.Header.Get("Authorization"))
			fmt.Fprint(w, `{"allowed":true}`)
		case "/api/image-models":
			require.Equal(t, "Bearer console-key", r.Header.Get("Authorization"))
			fmt.Fprintf(w, `{"items":[%s,{"id":"gpt-5.5","config_key":"gpt-5.5","mode":"text","enabled":true,"disable_api_key":true}]}`, wuzuTestModel)
		case "/api/me/site-api-keys":
			require.Equal(t, "Bearer console-key", r.Header.Get("Authorization"))
			if r.Method == http.MethodGet {
				if createdName != "" {
					writeSiteJSON(w, map[string]any{"items": []any{map[string]any{"name": createdName, "enabled": true}}})
				} else {
					fmt.Fprint(w, `{"items":[]}`)
				}
				return
			}
			creates++
			var input map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
			createdName = input["name"].(string)
			require.True(t, strings.HasPrefix(createdName, "s2site-"))
			require.Equal(t, []any{"gpt-image-2-web"}, input["allowed_model_config_keys"])
			require.Equal(t, "request", input["image_response_format_priority"])
			require.Equal(t, "b64_json", input["default_image_response_format"])
			require.Equal(t, float64(0), input["image_concurrency"])
			fmt.Fprint(w, `{"key":"inference-key","items":[]}`)
		default:
			t.Errorf("unexpected upstream operation %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	svc, _, accounts := siteTestService()
	rate := 100.0
	input := SiteInput{Name: "WUZU", Kind: "wuzu", BaseURL: server.URL, AuthMode: "password", Username: "member", Password: "console-password", Enabled: true, BalanceUnitsPerUSD: &rate}
	site, err := svc.Save(ctx, "", input)
	require.NoError(t, err)
	site, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.Empty(t, site.Error)
	require.Len(t, site.Models, 1)
	require.Equal(t, 2000, site.Concurrency.Limit)
	require.Equal(t, "gpt-image-2-web", site.Models[0].Model)
	require.Equal(t, "gpt-image-2", site.Models[0].Wuzu.Model)
	require.InDelta(t, .002, site.Models[0].Tiers[1].Prices["request"], 1e-12)
	require.Zero(t, creates, "catalogue sync must not create an inference key")
	site, err = svc.RefreshBalance(ctx, site.ID)
	require.NoError(t, err)
	require.Empty(t, site.Balance.Error)
	require.Equal(t, "额度", site.Balance.Currency)
	require.InDelta(t, 12.345, *site.Balance.AmountUSD, 1e-12)
	input.Password = ""
	site, err = svc.Bind(ctx, site.ID, SiteBinding{GroupID: PlatformOpenAI, Model: "gpt-image-2-web", LocalGroupID: 9, LocalModel: "local-image", Enabled: true})
	require.NoError(t, err)
	require.Empty(t, site.Bindings[0].Error)
	require.Equal(t, 1, creates)
	a, err := accounts.GetByID(ctx, site.Bindings[0].AccountID)
	require.NoError(t, err)
	require.Equal(t, "inference-key", a.GetCredential("api_key"))
	require.Equal(t, 2000, a.Concurrency)
	p, managed := a.SitePolicy()
	require.True(t, managed)
	require.Equal(t, "wuzu", p.SiteKind)
	require.Equal(t, "gpt-image-2-web", p.Wuzu.ConfigKey)
	credentials, err := svc.credentials(site)
	require.NoError(t, err)
	require.Equal(t, "u-abc123", credentials.SubjectID)
	credentials.AccessToken = "expired"
	adapter := newSiteAdapter(site, credentials)
	_, err = adapter.catalog(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, logins)
	key, err := adapter.ensureKey(ctx, &site.Bindings[0])
	require.NoError(t, err)
	require.Equal(t, "inference-key", key)
	require.Equal(t, 1, creates)
	raw, err := json.Marshal(site)
	require.NoError(t, err)
	for _, secret := range []string{"console-password", "console-key", "inference-key"} {
		require.NotContains(t, string(raw), secret)
	}
	rate = 50
	site, err = svc.Save(ctx, site.ID, input)
	require.NoError(t, err)
	require.Nil(t, site.LastSuccess, "conversion changes invalidate the old prices")
	a, _ = accounts.GetByID(ctx, a.ID)
	p, _ = a.SitePolicy()
	require.True(t, p.FreshUntil.IsZero())
	site, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.InDelta(t, .004, site.Models[0].Tiers[1].Prices["request"], 1e-12)
	rate = 0
	site, err = svc.Save(ctx, site.ID, input)
	require.NoError(t, err)
	site, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.NotEmpty(t, site.Models[0].Reason)
	require.Empty(t, site.Models[0].Tiers[0].Prices)
}

func TestWuzuCatalogPermissionsPricesAndConfigIdentity(t *testing.T) {
	for _, rate := range []float64{0, -1, math.NaN(), math.Inf(1), 100} {
		models, err := parseWuzuCatalog(gjson.Parse("["+wuzuTestModel+"]"), rate)
		require.NoError(t, err)
		require.Len(t, models, 1)
		if rate == 100 {
			require.Empty(t, models[0].Reason)
		} else {
			require.NotEmpty(t, models[0].Reason)
			require.Empty(t, models[0].Tiers[0].Prices)
		}
	}
	for _, patch := range []string{
		strings.Replace(wuzuTestModel, `"enabled":true`, `"enabled":false`, 1),
		strings.Replace(wuzuTestModel, `"disable_api_key":false`, `"disable_api_key":true`, 1),
		strings.Replace(wuzuTestModel, `"allowed_user_types":[]`, `"allowed_user_types":["unverified-type"]`, 1),
	} {
		models, err := parseWuzuCatalog(gjson.Parse("["+patch+"]"), 100)
		require.NoError(t, err)
		require.Empty(t, models)
	}
	second := strings.Replace(wuzuTestModel, `"config_key":"gpt-image-2-web"`, `"config_key":"gpt-image-2-firefly"`, 1)
	models, err := parseWuzuCatalog(gjson.Parse("["+wuzuTestModel+","+second+"]"), 100)
	require.NoError(t, err)
	require.Len(t, models, 2, "different configurations of one public model stay distinct")
	_, err = parseWuzuCatalog(gjson.Parse("["+wuzuTestModel+","+wuzuTestModel+"]"), 100)
	require.Error(t, err)
	partial := strings.Replace(wuzuTestModel, `"2k":0.2,"4k":0.3`, `"2k":null,"4k":-1`, 1)
	models, err = parseWuzuCatalog(gjson.Parse("["+partial+"]"), 100)
	require.NoError(t, err)
	require.NotEmpty(t, models[0].Tiers[1].Reason)
	require.Empty(t, models[0].Tiers[1].Prices)
	require.NotEmpty(t, models[0].Tiers[2].Reason)
}

func TestWuzuLostKeyResponseNeverCreatesDuplicate(t *testing.T) {
	created := false
	posts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/me":
			fmt.Fprint(w, `{"identity":{"id":"user-id","enabled":true,"effective_image_concurrency":3}}`)
		case "/api/me/site-api-keys/capability":
			fmt.Fprint(w, `{"allowed":true}`)
		case "/api/me/site-api-keys":
			if r.Method == http.MethodPost {
				created = true
				posts++
				w.WriteHeader(504)
			} else if created {
				fmt.Fprint(w, `{"items":[{"name":"s2site-binding","enabled":true}]}`)
			} else {
				fmt.Fprint(w, `{"items":[]}`)
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	models, err := parseWuzuCatalog(gjson.Parse("["+wuzuTestModel+"]"), 100)
	require.NoError(t, err)
	a := newSiteAdapter(&UpstreamSite{Kind: "wuzu", BaseURL: server.URL, AuthMode: "token", Models: models}, &SiteCredentials{AccessToken: "console-key"})
	b := &SiteBinding{ID: "binding", GroupID: PlatformOpenAI, Model: "gpt-image-2-web"}
	_, err = a.ensureKey(context.Background(), b)
	require.Error(t, err)
	_, err = a.ensureKey(context.Background(), b)
	require.ErrorContains(t, err, "不可再次读取")
	require.Equal(t, 1, posts)
	require.Empty(t, a.credentials.Keys)
}

func TestWuzuIdentityAndBalanceValidation(t *testing.T) {
	for _, profile := range []string{
		`{"id":"another-user","enabled":true,"effective_image_concurrency":2000}`,
		`{"id":"user-id","enabled":false,"effective_image_concurrency":2000}`,
		`{"id":"user-id","enabled":true,"effective_image_concurrency":0}`,
		`{"id":"user-id","enabled":true,"effective_image_concurrency":1.5}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprintf(w, `{"identity":%s}`, profile) }))
		a := newSiteAdapter(&UpstreamSite{Kind: "wuzu", BaseURL: server.URL, Bindings: []SiteBinding{{ID: "b"}}}, &SiteCredentials{SubjectID: "user-id", AccessToken: "console"})
		require.Error(t, a.authenticate(context.Background()))
		require.Nil(t, a.site.Concurrency)
		server.Close()
	}
	for _, suffix := range []string{`"unlimited":true,"remaining":null`, `"unlimited":false,"remaining":null`, `"unlimited":false,"remaining":"oops"`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"identity":{"id":"user-id","enabled":true,"effective_image_concurrency":2,%s}}`, suffix)
		}))
		a := newSiteAdapter(&UpstreamSite{Kind: "wuzu", BaseURL: server.URL}, &SiteCredentials{AccessToken: "console"})
		_, _, err := a.balance(context.Background())
		require.Error(t, err)
		server.Close()
	}
}

func TestWuzuSiteInputValidation(t *testing.T) {
	for _, tc := range []struct{ path, mode, token, refresh string }{
		{"/profile", "token", "console", ""},
		{"/v1", "token", "console", ""},
		{"", "token", "", "refresh"},
	} {
		svc, _, _ := siteTestService()
		_, err := svc.Save(context.Background(), "", SiteInput{Name: "WUZU", BaseURL: "https://example.test" + tc.path, Kind: "wuzu", AuthMode: tc.mode, AccessToken: tc.token, RefreshToken: tc.refresh})
		require.Error(t, err)
	}
}

func wuzuTestPolicy() SiteAccountPolicy {
	p := SiteAccountPolicy{SiteKind: "wuzu", BindingID: "b", Enabled: true, Image: true, FreshUntil: time.Now().Add(time.Minute), Wuzu: &WuzuModelConfig{ConfigKey: "gpt-image-2-web", Model: "gpt-image-2", SupportsEdit: true, SupportsUpscale: true, SizeMap: map[string]string{"16:9": "1536x864"}}}
	for i, tier := range []string{"1K", "2K", "4K"} {
		p.Tiers = append(p.Tiers, SitePriceTier{Key: tier, Unit: "USD/image", Prices: map[string]float64{"request": float64(i+1) * .01}})
		p.Limits = append(p.Limits, SiteTierLimit{Key: tier, Unit: "USD/image", Enabled: true, Limits: map[string]float64{"request": float64(i+1)*.01 + .005}})
	}
	return p
}

func TestWuzuPriceGateProtectsOutputOverridesAndLocalBilling(t *testing.T) {
	for _, tc := range []struct {
		body, tier string
		veto       bool
	}{
		{`{"size":"1024x1024"}`, "1K", false},
		{`{"size":"1K"}`, "1K", false},
		{`{"size":"1536x864"}`, "2K", false},
		{`{"size":"2560x1440"}`, "4K", false},
		{`{"size":"1024x1024","output_width":4096,"output_height":4096}`, "1K", true},
		{`{"size":"1024x1024","width":4096,"height":4096}`, "1K", true},
		{`{"size":"1024x1024","aspect_ratio":"1:1","resolution":"4k"}`, "1K", true},
		{`{"size":"1024x1024","async":true}`, "1K", true},
		{`{"size":"1024x1024","model_config_key":"other-model"}`, "1K", true},
		{`{"size":"1024x1024","n":10}`, "1K", true},
		{`{"size":"1024x1024","mask":{}}`, "1K", true},
		{`{"size":"1024x1024","size":"4096x4096"}`, "1K", true},
	} {
		t.Run(tc.body, func(t *testing.T) {
			p := wuzuTestPolicy()
			veto, _ := wuzuPriceVeto(p, SitePriceRequest{Tier: tc.tier, WuzuFields: wuzuRequestFields([]byte(tc.body), "application/json")})
			require.Equal(t, tc.veto, veto)
		})
	}
	p := wuzuTestPolicy()
	p.Limits[0].Enabled = false
	veto, reason := wuzuPriceVeto(p, SitePriceRequest{Tier: "2K", WuzuFields: map[string]string{"size": "1536x864"}})
	require.True(t, veto)
	require.Equal(t, "site_tier_disabled", reason, "WUZU purchases 1536px as 1K even though local billing calls it 2K")
}
