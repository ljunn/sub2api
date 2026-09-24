package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type siteMemoryRepo struct {
	mu    sync.Mutex
	sites map[string]*UpstreamSite
}

func (r *siteMemoryRepo) Lock(context.Context, string) (func(), error) {
	r.mu.Lock()
	return r.mu.Unlock, nil
}
func (r *siteMemoryRepo) Save(_ context.Context, s *UpstreamSite) error {
	raw, _ := json.Marshal(s)
	copy := &UpstreamSite{}
	_ = json.Unmarshal(raw, copy)
	copy.Secret = s.Secret
	r.sites[s.ID] = copy
	return nil
}
func (r *siteMemoryRepo) Get(_ context.Context, id string) (*UpstreamSite, error) {
	s := r.sites[id]
	if s == nil {
		return nil, errors.New("not found")
	}
	raw, _ := json.Marshal(s)
	copy := &UpstreamSite{}
	_ = json.Unmarshal(raw, copy)
	copy.Secret = s.Secret
	return copy, nil
}
func (r *siteMemoryRepo) List(ctx context.Context) ([]UpstreamSite, error) {
	out := []UpstreamSite{}
	for id := range r.sites {
		s, _ := r.Get(ctx, id)
		s.Secret = ""
		out = append(out, *s)
	}
	return out, nil
}
func (r *siteMemoryRepo) Delete(_ context.Context, id string) error { delete(r.sites, id); return nil }

type siteTestCipher struct{}

func (siteTestCipher) Encrypt(s string) (string, error) {
	return base64.StdEncoding.EncodeToString([]byte(s)), nil
}
func (siteTestCipher) Decrypt(s string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	return string(raw), err
}

type siteTestAccounts struct {
	AccountRepository
	accounts map[int64]*Account
	creates  int
}

func (r *siteTestAccounts) Create(_ context.Context, a *Account) error {
	r.creates++
	a.ID = int64(r.creates)
	r.accounts[a.ID] = a
	return nil
}
func (r *siteTestAccounts) GetByID(_ context.Context, id int64) (*Account, error) {
	a := r.accounts[id]
	if a == nil {
		return nil, ErrAccountNotFound
	}
	raw, _ := json.Marshal(a)
	var copy Account
	_ = json.Unmarshal(raw, &copy)
	return &copy, nil
}
func (r *siteTestAccounts) Update(_ context.Context, a *Account) error {
	r.accounts[a.ID] = a
	return nil
}
func (r *siteTestAccounts) UpdateExtra(_ context.Context, id int64, extra map[string]any) error {
	a := r.accounts[id]
	if a == nil {
		return ErrAccountNotFound
	}
	for k, v := range extra {
		a.Extra[k] = v
	}
	a.UpdatedAt = time.Now()
	return nil
}
func (r *siteTestAccounts) BindGroups(_ context.Context, id int64, groups []int64) error {
	r.accounts[id].GroupIDs = groups
	return nil
}
func (r *siteTestAccounts) FindByExtraField(_ context.Context, key string, value any) ([]Account, error) {
	out := []Account{}
	for _, a := range r.accounts {
		if a.Extra[key] == value {
			out = append(out, *a)
		}
	}
	return out, nil
}
func (r *siteTestAccounts) Delete(_ context.Context, id int64) error {
	delete(r.accounts, id)
	return nil
}

type siteTestAdmin struct{ AdminService }

func (siteTestAdmin) GetGroup(context.Context, int64) (*Group, error) {
	return &Group{ID: 9, Platform: PlatformOpenAI, Status: StatusActive}, nil
}
func (siteTestAdmin) ValidateAccountGroupBindings(context.Context, []int64) error { return nil }
func siteTestService() (*UpstreamSiteService, *siteMemoryRepo, *siteTestAccounts) {
	repo := &siteMemoryRepo{sites: map[string]*UpstreamSite{}}
	accounts := &siteTestAccounts{accounts: map[int64]*Account{}}
	svc := NewUpstreamSiteService(repo, accounts, siteTestAdmin{}, siteTestCipher{})
	svc.pricing, _ = siteTestPricing()
	return svc, repo, accounts
}
func writeSiteJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}
func TestUpstreamSiteLifecyclePriceChangeRecoveryAndStaleQueue(t *testing.T) {
	ctx := context.Background()
	price2K := 0.25
	concurrency := 5000
	failCatalog := false
	keyCreates := 0
	keyName := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/login":
			writeSiteJSON(w, map[string]any{"code": 0, "data": map[string]any{"access_token": "access", "refresh_token": "refresh", "user": map[string]any{"id": 7}}})
		case "/api/v1/auth/me":
			require.Equal(t, "Bearer access", r.Header.Get("Authorization"))
			writeSiteJSON(w, map[string]any{"code": 0, "data": map[string]any{"id": 7, "concurrency": concurrency}})
		case "/api/v1/groups/rates":
			writeSiteJSON(w, map[string]any{"code": 0, "data": map[string]float64{"2": 0.5}})
		case "/api/v1/model-plaza":
			if failCatalog {
				w.WriteHeader(503)
				return
			}
			fmt.Fprintf(w, `{"code":0,"data":{"groups":[{"id":2,"name":"images","rate_multiplier":2,"models":[{"name":"image-upstream","platform":"openai","pricing":{"billing_mode":"image","intervals":[{"tier_label":"1K","per_request_price":0.2},{"tier_label":"2K","per_request_price":%g},{"tier_label":"4K","per_request_price":0.8}]}}]}]}}`, price2K*2)
		case "/api/v1/keys":
			if r.Method == http.MethodPost {
				keyCreates++
				var input map[string]any
				require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
				keyName = input["name"].(string)
				assert.Equal(t, float64(2), input["group_id"])
				writeSiteJSON(w, map[string]any{"code": 0, "data": map[string]any{"key": "sk-generated"}})
			} else {
				items := []any{}
				if keyName != "" {
					items = append(items, map[string]any{"name": keyName, "group_id": 2, "key": "sk-generated"})
				}
				writeSiteJSON(w, map[string]any{"code": 0, "data": map[string]any{"items": items}})
			}
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	svc, repo, accounts := siteTestService()
	ctx = svc.WithPricingContext(ctx)
	svc.preview = false
	site, err := svc.Save(ctx, "", SiteInput{Name: "A", BaseURL: server.URL, Kind: "sub2api", AuthMode: "password", Username: "user@example.test", Password: "private-password", Enabled: true})
	require.NoError(t, err)
	site, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.Empty(t, site.Error)
	require.Len(t, site.Models, 1)
	assert.InDelta(t, 0.1, site.Models[0].Tiers[0].Prices["request"], 1e-12, "personal rate replaces the public group rate")
	b := SiteBinding{GroupID: "2", Model: "image-upstream", LocalGroupID: 9, LocalModel: "my-image", Enabled: true, Limits: []SiteTierLimit{{Key: "1K", Unit: "USD/image", Enabled: true, Limits: map[string]float64{"request": 9999}}, {Key: "2K", Unit: "USD/image", Enabled: true, Limits: map[string]float64{"request": 0.20}}, {Key: "4K", Unit: "USD/image", Enabled: true, Limits: map[string]float64{"request": 0.5}}}}
	site, err = svc.Bind(ctx, site.ID, b)
	require.NoError(t, err)
	require.Len(t, site.Bindings, 1)
	require.Empty(t, site.Bindings[0].Error)
	assert.Equal(t, "partial", site.Bindings[0].Status)
	account, err := accounts.GetByID(ctx, site.Bindings[0].AccountID)
	require.NoError(t, err)
	assert.Equal(t, 5000, account.Concurrency, "new binding uses the upstream profile limit")
	assert.True(t, account.IsPoolMode())
	assert.Zero(t, account.GetPoolModeRetryCount(), "new site bindings must not retry a slow failed upstream")
	assert.Equal(t, "image-upstream", account.GetMappedModel("my-image"))
	assert.False(t, account.IsModelSupported("unbound-model"))
	for size, want := range map[string]bool{"1K": false, "2K": true, "4K": false, "auto": true, "nonsense": true} {
		veto, _ := SitePriceVeto(WithSiteImageSize(ctx, size), account)
		assert.Equal(t, want, veto, size)
	}
	_, err = svc.Bind(ctx, site.ID, b)
	require.NoError(t, err)
	assert.Equal(t, 1, keyCreates)
	assert.Equal(t, 1, accounts.creates)
	// Price recovers on the next successful scan.
	price2K = 0.18
	concurrency = 150
	site, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	assert.Equal(t, 150, accounts.accounts[account.ID].Concurrency, "existing bindings follow upstream limit changes")
	assert.Equal(t, "ready", site.Bindings[0].Status)
	require.Len(t, site.History, 1)
	require.NoError(t, CheckSitePriceBeforeSend(WithSiteImageSize(ctx, "2K"), account, accounts), "queued request sees latest authoritative price")
	price2K = 0.30
	_, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	require.Error(t, CheckSitePriceBeforeSend(WithSiteImageSize(ctx, "2K"), account, accounts))
	lastSuccess := *repo.sites[site.ID].LastSuccess
	failCatalog = true
	site, err = svc.Sync(ctx, site.ID)
	require.NoError(t, err)
	assert.NotEmpty(t, site.Error)
	assert.Equal(t, lastSuccess, *site.LastSuccess)
	policy, _ := accounts.accounts[account.ID].SitePolicy()
	assert.Equal(t, "site_price_expired", siteTierReason(policy, "1K", lastSuccess.Add(10*time.Minute)))
	raw, err := json.Marshal(site)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "private-password")
	assert.NotContains(t, string(raw), "sk-generated")
	assert.NotContains(t, string(raw), "refresh_token")
	// Turning off a binding is possible even with a missing/stale catalogue.
	repo.sites[site.ID].Models = nil
	b = site.Bindings[0]
	b.Enabled = false
	site, err = svc.Bind(ctx, site.ID, b)
	require.NoError(t, err)
	assert.Equal(t, "blocked", site.Bindings[0].Status)
}
func TestUpstreamSiteRefreshTokenRotationPersistsOnCatalogueFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Equal(t, "old-refresh", body["refresh_token"])
			writeSiteJSON(w, map[string]any{"code": 0, "data": map[string]string{"access_token": "new-access", "refresh_token": "new-refresh"}})
		default:
			w.WriteHeader(503)
		}
	}))
	defer server.Close()
	svc, repo, _ := siteTestService()
	site, err := svc.Save(context.Background(), "", SiteInput{Name: "refresh", BaseURL: server.URL, Kind: "sub2api", AuthMode: "token", RefreshToken: "old-refresh", Enabled: true})
	require.NoError(t, err)
	result, err := svc.Sync(context.Background(), site.ID)
	require.NoError(t, err)
	require.NotEmpty(t, result.Error)
	credentials, err := svc.credentials(repo.sites[site.ID])
	require.NoError(t, err)
	assert.Equal(t, "new-refresh", credentials.RefreshToken)
	assert.Equal(t, "new-access", credentials.AccessToken)
}
func TestUpstreamSiteNewAPIPasswordEncryptionAndCookieRefresh(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	der, err := x509.MarshalPKIXPublicKey(&private.PublicKey)
	require.NoError(t, err)
	public := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	refreshCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/login/encryption-key":
			writeSiteJSON(w, map[string]any{"success": true, "data": map[string]any{"enabled": true, "kid": "k1", "public_key": public}})
		case "/api/user/login":
			var body map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			assert.Empty(t, body["password"])
			assert.Equal(t, "k1", body["encryption_key_id"])
			cipher, err := base64.StdEncoding.DecodeString(body["password_encrypted"])
			require.NoError(t, err)
			plain, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, private, cipher, nil)
			require.NoError(t, err)
			assert.Equal(t, "secret", string(plain))
			http.SetCookie(w, &http.Cookie{Name: "new_api_refresh", Value: "cookie-refresh", Path: "/"})
			writeSiteJSON(w, map[string]any{"success": true, "data": map[string]any{"access_token": "access", "user": map[string]int{"id": 12}}})
		case "/api/user/self":
			if r.Header.Get("Authorization") == "Bearer expired" {
				w.WriteHeader(401)
				return
			}
			writeSiteJSON(w, map[string]any{"success": true, "data": map[string]any{"id": 12}})
		case "/api/user/auth/refresh":
			refreshCalls++
			cookie, err := r.Cookie("new_api_refresh")
			require.NoError(t, err)
			assert.Equal(t, "cookie-refresh", cookie.Value)
			assert.Equal(t, "http://"+r.Host, r.Header.Get("Origin"))
			http.SetCookie(w, &http.Cookie{Name: "new_api_refresh", Value: "rotated", Path: "/"})
			writeSiteJSON(w, map[string]any{"success": true, "data": map[string]any{"access_token": "renewed", "user": map[string]int{"id": 12}}})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	site := &UpstreamSite{BaseURL: server.URL, Kind: "newapi", AuthMode: "password", Username: "operator"}
	credentials := &SiteCredentials{Password: "secret"}
	adapter := newSiteAdapter(site, credentials)
	require.NoError(t, adapter.authenticate(context.Background()))
	assert.Equal(t, int64(12), site.UserID)
	assert.Equal(t, "cookie-refresh", credentials.RefreshToken)
	credentials.AccessToken = "expired"
	require.NoError(t, adapter.authenticate(context.Background()))
	assert.Equal(t, 1, refreshCalls)
	assert.Equal(t, "renewed", credentials.AccessToken)
	assert.Equal(t, "rotated", credentials.RefreshToken)
}
func TestUpstreamSiteNewAPIKeyRecoveryAfterAmbiguousCreation(t *testing.T) {
	count := 0
	exists := false
	name := "s2site-b1"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/self":
			fmt.Fprint(w, `{"success":true,"data":{"id":1}}`)
		case "/api/token/search":
			items := []any{}
			if exists {
				items = append(items, map[string]any{"id": 41, "name": name, "group": "vip"})
			}
			writeSiteJSON(w, map[string]any{"success": true, "data": map[string]any{"items": items}})
		case "/api/token/":
			count++
			exists = true
			var input map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&input))
			assert.Equal(t, "vip", input["group"])
			assert.Equal(t, false, input["cross_group_retry"])
			assert.Equal(t, "upstream-alias", input["model_limits"])
			w.WriteHeader(503)
		case "/api/token/41/key":
			assert.Equal(t, http.MethodPost, r.Method)
			fmt.Fprint(w, `{"success":true,"data":{"key":"complete-key"}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	adapter := newSiteAdapter(&UpstreamSite{BaseURL: server.URL, Kind: "newapi", AuthMode: "token"}, &SiteCredentials{AccessToken: "token"})
	binding := &SiteBinding{ID: "b1", GroupID: "vip", Model: "upstream-alias"}
	_, err := adapter.ensureKey(context.Background(), binding)
	require.Error(t, err)
	key, err := adapter.ensureKey(context.Background(), binding)
	require.NoError(t, err)
	assert.Equal(t, "sk-complete-key", key)
	assert.Equal(t, 1, count)
}
func TestUpstreamSitePriceParsingAndExpressionBounds(t *testing.T) {
	result := gjson.Parse(`{"data":[{"model_name":"text","model_ratio":1.5,"completion_ratio":5,"cache_ratio":0.1,"enable_groups":["vip"]},{"model_name":"free","model_price":0,"quota_type":1,"enable_groups":["vip"]},{"model_name":"missing","quota_type":1,"enable_groups":["vip"]}],"group_ratio":{"vip":0.5},"usable_group":{"vip":"VIP"}}`)
	models, err := parseNewAPISiteCatalog(result)
	require.NoError(t, err)
	byName := map[string]SiteModel{}
	for _, m := range models {
		byName[m.Model] = m
	}
	assert.Equal(t, map[string]float64{"input_price": 1.5, "output_price": 7.5, "cache_read_price": 0.15000000000000002}, byName["text"].Tiers[0].Prices)
	assert.Equal(t, 0.0, byName["free"].Tiers[0].Prices["request"])
	assert.NotEmpty(t, byName["missing"].Reason)
	tier, err := siteExpressionPrices(`v1: (len > 200000 ? tier("long", p*6+c*20) : tier("base",p*3+c*15+cr*0.3)) * (hour("UTC") > 8 ? 2 : 1)`, 0.5)
	require.NoError(t, err)
	assert.Equal(t, map[string]float64{"input_price": 6, "output_price": 20, "cache_read_price": 0.3}, tier.Prices)
	for _, expression := range []string{`p*c`, `p / (hour("UTC") > 8 ? 2 : 1)`, `p * -1`, `u("seconds")*0.2`, `v2: p*3`, `p*3|||when(true)*4`} {
		_, err = siteExpressionPrices(expression, 1)
		assert.Error(t, err, expression)
	}
}
func TestUpstreamSiteNeverDisclosesCredentialOnRemoteErrorsOrRedirects(t *testing.T) {
	reached := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, other.URL, 302) }))
	defer server.Close()
	adapter := newSiteAdapter(&UpstreamSite{BaseURL: server.URL, Kind: "sub2api"}, &SiteCredentials{AccessToken: "secret-access"})
	_, err := adapter.request(context.Background(), http.MethodGet, "/api/v1/auth/me", nil)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-access")
	assert.False(t, reached)
}
func TestUpstreamSitePolicyUnknownExpiredAndManualPause(t *testing.T) {
	p := SiteAccountPolicy{BindingID: "b", Enabled: true, FreshUntil: time.Now().Add(time.Minute), Tiers: []SitePriceTier{{Key: "default", Unit: "USD/request", Prices: map[string]float64{"request": 0}}}, Limits: []SiteTierLimit{{Key: "default", Unit: "USD/request", Enabled: true, AllowEqualPriceScheduling: true, Limits: map[string]float64{"request": 0}}}}
	raw, _ := json.Marshal(p)
	var extra any
	require.NoError(t, json.Unmarshal(raw, &extra))
	a := &Account{ID: 1, Status: StatusActive, Schedulable: true, Credentials: map[string]any{SiteBindingCredentialKey: "b"}, Extra: map[string]any{SitePolicyExtraKey: extra}}
	veto, _ := SitePriceVeto(context.Background(), a)
	assert.False(t, veto, "explicit free is valid")
	a.Schedulable = false
	require.Error(t, CheckSitePriceBeforeSend(context.Background(), a, nil))
	a.Schedulable = true
	delete(a.Extra, SitePolicyExtraKey)
	veto, reason := SitePriceVeto(context.Background(), a)
	assert.True(t, veto)
	assert.True(t, strings.HasPrefix(reason, "site_"))
	assert.False(t, a.IsSchedulable())
}

func TestUpstreamSiteSub2APIIntervalMultipliersAndIndependentImageRate(t *testing.T) {
	groups := gjson.Parse(`[{"id":2,"rate_multiplier":0.5,"image_rate_independent":true,"image_rate_multiplier":2,"models":[{"name":"image","pricing":{"billing_mode":"image","per_request_price":0.1},"time_pricing":{"periods":[{"multiplier":3}]}},{"name":"text","pricing":{"billing_mode":"token","input_price":0.000002,"output_price":0.000006,"cache_write_price":0.0000025,"cache_write_1h_price":0.000004,"intervals":[{"input_multiplier":2,"output_multiplier":3,"cache_write_price":0.00001,"cache_write_1h_price":null}]}}]}]`)
	models, err := parseSub2APISiteCatalog(groups, gjson.Parse(`{}`))
	require.NoError(t, err)
	require.Len(t, models, 2)
	require.InDelta(t, 0.6, models[0].Tiers[0].Prices["request"], 1e-12)
	require.Equal(t, map[string]float64{"input_price": 2, "output_price": 9, "cache_write_price": 5, "cache_write_1h_price": 5}, models[1].Tiers[0].Prices)
}

func TestUpstreamSiteSub2APIGenericReasoningCostCeiling(t *testing.T) {
	for _, tc := range []struct {
		pricing string
		input   float64
	}{
		{`"max_reasoning_effort_multiplier":2`, 2},
		{`"max_reasoning_effort_multiplier":2,"reasoning_effort_multipliers":{"max":3,"high":4}`, 4},
		{`"max_reasoning_effort_multiplier":2,"reasoning_effort_multipliers":{}`, 1},
	} {
		groups := gjson.Parse(`[{"id":1,"rate_multiplier":1,"models":[{"name":"text","pricing":{"billing_mode":"token","input_price":0.000001,"output_price":0.000002,` + tc.pricing + `}}]}]`)
		models, err := parseSub2APISiteCatalog(groups, gjson.Parse(`{}`))
		require.NoError(t, err)
		require.Len(t, models, 1)
		require.Equal(t, tc.input, models[0].Tiers[0].Prices["input_price"])
		require.Equal(t, tc.input*2, models[0].Tiers[0].Prices["output_price"])
	}
}
