package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestKongfangLifecycle(t *testing.T) {
	ctx := context.Background()
	logins, creates := 0, 0
	createdName := ""
	discount := .5
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/admin/auth/login":
			logins++
			var in map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
			require.Equal(t, map[string]string{"username": "user@example.test", "password": "console-password"}, in)
			fmt.Fprint(w, `{"code":0,"data":{"token":"console-token","user":{"id":54}}}`)
		case "/api/v1/user/profile":
			if r.Header.Get("Authorization") != "Bearer console-token" {
				w.WriteHeader(401)
				return
			}
			fmt.Fprintf(w, `{"code":0,"data":{"id":54,"status":"active","is_vip":true,"vip_discount":%g}}`, discount)
		case "/api/v1/user/pricing":
			require.Equal(t, "Bearer console-token", r.Header.Get("Authorization"))
			fmt.Fprint(w, `{"code":0,"data":{"image_gpt":{"quality_1k":0.02,"quality_2k":0.04,"quality_4k":0.06},"image_std":{"quality_1k":0.02,"quality_2k":0.04,"quality_4k":0.06}}}`)
		case "/api/v1/user/models":
			fmt.Fprint(w, `{"code":0,"data":{"image":[{"id":"gpt-image-2","kind":"image","enabled":true},{"id":"gemini-3.1-flash-image","kind":"image","enabled":true}],"video":[],"chat":[]}}`)
		case "/api/v1/user/api-keys":
			require.Equal(t, "Bearer console-token", r.Header.Get("Authorization"))
			if r.Method == http.MethodPost {
				creates++
				var in map[string]string
				require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
				createdName = in["name"]
				w.WriteHeader(504) // Created remotely, but the response was lost.
			} else {
				items := []map[string]any{{"name": "existing", "is_active": true, "key_raw": "sk-discovery"}}
				if createdName != "" {
					items = append(items, map[string]any{"name": createdName, "is_active": true, "key_raw": "sk-binding"})
				}
				writeSiteJSON(w, map[string]any{"code": 0, "data": items})
			}
		case "/v1/models":
			require.Equal(t, "Bearer sk-discovery", r.Header.Get("Authorization"))
			fmt.Fprint(w, `{"data":[{"id":"gpt-image-2","type":"image"},{"id":"sora2","type":"video"}]}`)
		case "/v1beta/models":
			require.Empty(t, r.Header.Get("Authorization"))
			require.Equal(t, "sk-discovery", r.Header.Get("x-goog-api-key"))
			fmt.Fprint(w, `{"models":[{"name":"models/gemini-3.1-flash-image-preview"}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	svc, _, accounts := siteTestService()
	svc.preview = true
	input := SiteInput{Name: "空凡", BaseURL: server.URL, Kind: "kongfang", AuthMode: "password", Username: "user@example.test", Password: "console-password", Enabled: true, CreditUSD: .1}
	site, err := svc.Save(ctx, "", input)
	require.NoError(t, err)
	site, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.Empty(t, site.Error)
	require.Len(t, site.Models, 2)
	require.Equal(t, 1, logins)
	require.Zero(t, creates)
	m := findSiteModel(site, "openai", "gpt-image-2")
	require.NotNil(t, m)
	for i, want := range []float64{.001, .002, .003} {
		require.InDelta(t, want, m.Tiers[i].Prices["request"], 1e-12)
	}
	b := SiteBinding{GroupID: "openai", Model: "gpt-image-2", LocalGroupID: 9, LocalModel: "my-image", Enabled: true}
	site, err = svc.Bind(ctx, site.ID, b)
	require.NoError(t, err)
	require.NotEmpty(t, site.Bindings[0].Error)
	require.Equal(t, 1, creates)
	b = site.Bindings[0]
	site, err = svc.Bind(ctx, site.ID, b)
	require.NoError(t, err)
	require.Empty(t, site.Bindings[0].Error)
	require.Equal(t, 1, creates, "recover the named key after a lost response")
	a, err := accounts.GetByID(ctx, site.Bindings[0].AccountID)
	require.NoError(t, err)
	require.Equal(t, StatusDisabled, a.Status)
	require.Equal(t, "sk-binding", a.GetCredential("api_key"))
	policy, ok := a.SitePolicy()
	require.True(t, ok)
	require.Equal(t, "kongfang", policy.SiteKind)
	raw, err := json.Marshal(site)
	require.NoError(t, err)
	for _, secret := range []string{"console-password", "console-token", "sk-discovery", "sk-binding"} {
		require.NotContains(t, string(raw), secret)
	}
	input.Password = ""
	input.UserID = 54
	input.CreditUSD = .2
	site, err = svc.Save(ctx, site.ID, input)
	require.NoError(t, err)
	require.Nil(t, site.LastSuccess)
	require.Empty(t, site.Models)
	a, err = accounts.GetByID(ctx, a.ID)
	require.NoError(t, err)
	policy, _ = a.SitePolicy()
	require.True(t, policy.FreshUntil.IsZero())
	discount = 1
	site, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.Empty(t, site.Error)
	require.InDelta(t, .008, findSiteModel(site, "openai", "gpt-image-2").Tiers[1].Prices["request"], 1e-12)
	credentials, err := svc.credentials(site)
	require.NoError(t, err)
	credentials.AccessToken = "expired"
	adapter := newSiteAdapter(site, credentials)
	_, err = adapter.catalog(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, logins)
	discount = 0
	_, err = adapter.catalog(ctx)
	require.ErrorContains(t, err, "VIP")
}

func TestKongfangMissingPricesAndConversion(t *testing.T) {
	available := gjson.Parse(`[{"id":"gpt-image-2","kind":"image","enabled":true},{"id":"future-image","kind":"image","enabled":true}]`)
	openai := gjson.Parse(`[{"id":"gpt-image-2","type":"image"},{"id":"future-image","type":"image"},{"id":"disabled-image","type":"image"}]`)
	prices := gjson.Parse(`{"image_gpt":{"quality_1k":0.02,"quality_2k":null,"quality_4k":-1}}`)
	for _, conversion := range []float64{0, math.NaN(), math.Inf(1), .1} {
		out := parseKongfangCatalog(available, openai, gjson.Parse(`[]`), prices, .5, conversion)
		require.Len(t, out, 2)
		require.NotEmpty(t, out[0].Reason)
		require.NotEmpty(t, out[1].Tiers[1].Reason)
		require.Empty(t, out[1].Tiers[1].Prices)
		require.NotEmpty(t, out[1].Tiers[2].Reason)
		if conversion == .1 {
			require.InDelta(t, .001, out[1].Tiers[0].Prices["request"], 1e-12)
		} else {
			require.NotEmpty(t, out[1].Reason)
			require.Empty(t, out[1].Tiers[0].Prices)
		}
	}
}

func TestKongfangValidation(t *testing.T) {
	for _, tc := range []struct {
		in   SiteInput
		want string
	}{
		{SiteInput{BaseURL: "https://example.test/user/balance"}, "首页"},
		{SiteInput{BaseURL: "https://example.test", CreditUSD: -1}, "积分"},
		{SiteInput{BaseURL: "https://example.test", AuthMode: "token", RefreshToken: "refresh"}, "Access Token"},
	} {
		svc, _, _ := siteTestService()
		in := tc.in
		in.Kind = "kongfang"
		in.Name = "test"
		if in.AuthMode == "" {
			in.AuthMode = "password"
			in.Username = "u"
			in.Password = "p"
		}
		_, err := svc.Save(context.Background(), "", in)
		require.ErrorContains(t, err, tc.want)
	}
}

func TestKongfangTransport(t *testing.T) {
	for _, tc := range []struct{ body, tier string }{
		{`{"size":"1280x720"}`, "1K"}, {`{"size":"2560x1440"}`, "2K"}, {`{"size":"3840x2160"}`, "4K"}, {`{}`, "2K"},
		{`{"size":"1024x1024","quality":"4k"}`, "4K"}, {`{"resolution":"2k","quality":"4k"}`, "unknown"}, {`{"quality":"high"}`, "unknown"}, {`{"resolution":null}`, "unknown"},
		{`{"contents":[],"generationConfig":{"imageConfig":{"imageSize":"1K"}}}`, "1K"},
	} {
		require.Equal(t, tc.tier, kongfangRequestTier([]byte(tc.body)), tc.body)
	}
	for _, tc := range []struct{ path, body, quality, resolution, ratio string }{
		{"/v1/images/generations", `{"model":"gpt-image-2","prompt":"test","size":"2560x1440"}`, "2k", "", ""},
		{"/v1beta/models/gemini-3.1-flash-image:generateContent", `{"contents":[],"generationConfig":{"imageConfig":{"imageSize":"4K","aspectRatio":"16:9"}}}`, "", "4k", "16:9"},
	} {
		req, _ := http.NewRequest(http.MethodPost, "https://example.test"+tc.path, strings.NewReader(tc.body))
		require.NoError(t, prepareKongfangRequest(req))
		raw, _ := io.ReadAll(req.Body)
		require.Equal(t, tc.quality, gjson.GetBytes(raw, "quality").String())
		require.Equal(t, tc.resolution, gjson.GetBytes(raw, "generationConfig.resolution").String())
		require.Equal(t, tc.ratio, gjson.GetBytes(raw, "generationConfig.aspectRatio").String())
		require.Equal(t, int64(len(raw)), req.ContentLength)
		require.NoError(t, prepareKongfangRequest(req))
		again, _ := io.ReadAll(req.Body)
		require.Equal(t, string(raw), string(again))
	}
	for _, path := range []string{"/v1/images/edits", "/v1/responses", "/v1/chat/completions", "/v1/video/generations"} {
		req, _ := http.NewRequest(http.MethodPost, "https://example.test"+path, strings.NewReader(`{}`))
		require.Error(t, prepareKongfangRequest(req))
	}
}

func TestKongfangPriceGate(t *testing.T) {
	p := SiteAccountPolicy{SiteKind: "kongfang", BindingID: "b", Enabled: true, FreshUntil: time.Now().Add(time.Minute)}
	for i, tier := range []string{"1K", "2K", "4K"} {
		p.Tiers = append(p.Tiers, SitePriceTier{Key: tier, Unit: "USD/image", Prices: map[string]float64{"request": float64(i+1) * .01}})
		p.Limits = append(p.Limits, SiteTierLimit{Key: tier, Unit: "USD/image", Enabled: true, Limits: map[string]float64{"request": float64(i+1) * .015}})
	}
	a := siteAutomaticAccount(p)
	for _, tc := range []struct {
		body string
		veto bool
	}{
		{`{"size":"1024x1024","quality":"1k"}`, false}, {`{"size":"1024x1024","quality":"4k"}`, true}, {`{"size":"2560x1440","quality":"2k"}`, false}, {`{"quality":"high"}`, true}, {`{"contents":[],"generationConfig":{"resolution":"1k"}}`, false},
	} {
		veto, _ := SitePriceVeto(WithSitePriceRequest(context.Background(), []byte(tc.body)), a)
		require.Equal(t, tc.veto, veto, tc.body)
	}
	p.Limits[0].Enabled = false
	a = siteAutomaticAccount(p)
	veto, _ := SitePriceVeto(WithSitePriceRequest(context.Background(), []byte(`{"size":"2560x1440","quality":"2k"}`)), a)
	require.False(t, veto)
	veto, _ = SitePriceVeto(WithSitePriceRequest(context.Background(), []byte(`{"size":"1024x1024","quality":"1k"}`)), a)
	require.True(t, veto)
}
