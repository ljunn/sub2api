package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNewAPISiteBalanceDisplayUnits(t *testing.T) {
	for _, tt := range []struct {
		name, profile, status, currency string
		amount                          float64
		invalid                         bool
	}{
		{"USD", `{"quota":10000000}`, `{"quota_per_unit":500000,"quota_display_type":"USD"}`, "USD", 20, false},
		{"CNY", `{"quota":1000000}`, `{"quota_per_unit":500000,"quota_display_type":"CNY","usd_exchange_rate":7}`, "CNY", 14, false},
		{"site rate one", `{"quota":9500000}`, `{"quota_per_unit":500000,"quota_display_type":"CNY","usd_exchange_rate":1}`, "CNY", 19, false},
		{"custom", `{"quota":1000000}`, `{"quota_per_unit":500000,"quota_display_type":"CUSTOM","custom_currency_symbol":"EUR","custom_currency_exchange_rate":0.9}`, "EUR", 1.8, false},
		{"quota display", `{"quota":19}`, `{"quota_display_type":"TOKENS"}`, "QUOTA", 19, false},
		{"legacy currency", `{"quota":"0"}`, `{"quota_per_unit":500000,"display_in_currency":true}`, "USD", 0, false},
		{"legacy quota", `{"quota":-1}`, `{"display_in_currency":false}`, "QUOTA", -1, false},
		{"negative debt", `{"quota":-500000}`, `{"quota_per_unit":500000,"quota_display_type":"USD"}`, "USD", -1, false},
		{"missing quota", `{}`, `{"quota_per_unit":500000,"quota_display_type":"USD"}`, "", 0, true},
		{"null quota", `{"quota":null}`, `{"quota_per_unit":500000,"quota_display_type":"USD"}`, "", 0, true},
		{"bad quota", `{"quota":"oops"}`, `{"quota_per_unit":500000,"quota_display_type":"USD"}`, "", 0, true},
		{"infinite quota", `{"quota":"Inf"}`, `{"quota_per_unit":500000,"quota_display_type":"USD"}`, "", 0, true},
		{"missing divisor", `{"quota":10}`, `{"quota_display_type":"USD"}`, "", 0, true},
		{"zero divisor", `{"quota":10}`, `{"quota_per_unit":0,"quota_display_type":"USD"}`, "", 0, true},
		{"missing rate", `{"quota":10}`, `{"quota_per_unit":500000,"quota_display_type":"CNY"}`, "", 0, true},
		{"negative rate", `{"quota":10}`, `{"quota_per_unit":500000,"quota_display_type":"CNY","usd_exchange_rate":-7}`, "", 0, true},
		{"missing unit", `{"quota":10}`, `{}`, "", 0, true},
		{"unknown unit", `{"quota":10}`, `{"quota_per_unit":500000,"quota_display_type":"???"}`, "", 0, true},
		{"overflow", `{"quota":1e308}`, `{"quota_per_unit":0.001,"quota_display_type":"USD"}`, "", 0, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			amount, currency, err := parseNewAPISiteBalance(gjson.Parse(tt.profile), gjson.Parse(tt.status))
			if tt.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.amount, amount)
			require.Equal(t, tt.currency, currency)
		})
	}
}

type siteBalanceSettingRepo struct {
	SettingRepository
	values map[string]string
}

func (r *siteBalanceSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return r.values, nil
}
func (r *siteBalanceSettingRepo) Set(_ context.Context, key, value string) error {
	r.values[key] = value
	return nil
}

type siteBalanceTestMailer struct {
	to, body string
	calls    int
	err      error
}

func (m *siteBalanceTestMailer) SendEmail(_ context.Context, to, subject, body string) error {
	m.to, m.body = to, body
	m.calls++
	return m.err
}

func balanceTestSettings() *siteBalanceSettingRepo {
	return &siteBalanceSettingRepo{values: map[string]string{
		siteBalanceSettingsKey: `{"enabled":true,"threshold":20,"admin_email":"admin@example.com"}`,
		SettingKeySMTPHost:     "smtp.example.com",
		SettingKeySMTPFrom:     "sender@example.com",
	}}
}

func TestSiteBalanceQueryNotificationLifecycle(t *testing.T) {
	ctx := context.Background()
	profile := `{"data":{"id":1,"balance":20}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/auth/me", r.URL.Path)
		require.Equal(t, "Bearer secret-token", r.Header.Get("Authorization"))
		_, _ = fmt.Fprint(w, profile)
	}))
	defer server.Close()
	svc, repo, _ := siteTestService()
	mailer := &siteBalanceTestMailer{}
	svc.balanceSettings, svc.balanceMailer = balanceTestSettings(), mailer
	site, err := svc.Save(ctx, "", SiteInput{Name: "<b>Site</b>", BaseURL: server.URL, Kind: "sub2api", AuthMode: "token", AccessToken: "secret-token", Enabled: true})
	require.NoError(t, err)
	query := func() *UpstreamSite {
		t.Helper()
		result, err := svc.RefreshBalance(ctx, site.ID)
		require.NoError(t, err)
		return result
	}
	result := query()
	require.Empty(t, result.Balance.Error)
	require.Equal(t, 20.0, *result.Balance.Amount)
	require.Zero(t, mailer.calls, "exactly 20 must not alert")
	profile = `{"data":{"id":1,"balance":19.99}}`
	result = query()
	require.Equal(t, 1, mailer.calls)
	require.Equal(t, "admin@example.com", mailer.to)
	require.Contains(t, mailer.body, "19.99 USD")
	require.Contains(t, mailer.body, "&lt;b&gt;Site&lt;/b&gt;")
	require.NotContains(t, mailer.body, "secret-token")
	require.NotNil(t, result.Balance.LastNotified)
	query()
	require.Equal(t, 1, mailer.calls, "manual requery must not duplicate mail")
	// Durable state survives service restarts, independently of Redis DB selection.
	svc = NewUpstreamSiteService(repo, nil, nil, siteTestCipher{})
	svc.balanceSettings, svc.balanceMailer = balanceTestSettings(), mailer
	query()
	require.Equal(t, 1, mailer.calls)
	past := time.Now().Add(-time.Second)
	repo.sites[site.ID].Balance.NextNotify = &past
	query()
	require.Equal(t, 2, mailer.calls, "sustained low balances repeat after the cooldown")
	profile = `{"data":{"id":1,"balance":null}}`
	result = query()
	require.NotEmpty(t, result.Balance.Error)
	require.Equal(t, 19.99, *result.Balance.Amount, "failed queries retain the last known value")
	require.Equal(t, 2, mailer.calls)
	profile = `{"data":{"id":1,"balance":20}}`
	result = query()
	require.Nil(t, result.Balance.LowSince)
	require.Nil(t, result.Balance.NextNotify)
	profile = `{"data":{"id":1,"balance":0}}`
	result = query()
	require.Equal(t, 0.0, *result.Balance.Amount)
	require.Equal(t, 3, mailer.calls, "a new drop after recovery must notify immediately")
	result.Enabled = false
	require.NoError(t, repo.Save(ctx, result))
	repo.sites[site.ID].Balance.NextNotify = &past
	query()
	require.Equal(t, 3, mailer.calls, "disabled sites can be queried without sending alerts")
}

func TestSiteBalanceMailFailureRetriesAndDoesNotLeak(t *testing.T) {
	ctx := context.Background()
	svc, repo, _ := siteTestService()
	mailer := &siteBalanceTestMailer{err: errors.New("smtp password=secret response")}
	svc.balanceSettings, svc.balanceMailer = balanceTestSettings(), mailer
	now := time.Now().UTC()
	amount := -1.0
	site := &UpstreamSite{ID: "test", Name: "test", Balance: &SiteBalance{Amount: &amount, Currency: "CNY"}}
	require.NoError(t, svc.notifySiteBalance(ctx, site, now))
	require.Equal(t, 1, mailer.calls)
	require.Nil(t, site.Balance.LastNotified)
	require.NotContains(t, site.Balance.NotifyError, "secret")
	mailer.err = nil
	require.NoError(t, svc.notifySiteBalance(ctx, site, now.Add(time.Minute)))
	require.Equal(t, 1, mailer.calls)
	require.NoError(t, svc.notifySiteBalance(ctx, site, now.Add(10*time.Minute)))
	require.Equal(t, 2, mailer.calls)
	require.NotNil(t, repo.sites[site.ID].Balance.LastNotified)
	require.Empty(t, repo.sites[site.ID].Balance.NotifyError)
}

func TestSiteBalanceRefreshPersistsRotatedTokensWithoutCatalogue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/me":
			if r.Header.Get("Authorization") != "Bearer fresh-token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = fmt.Fprint(w, `{"data":{"id":1,"balance":"15.5"}}`)
		case "/api/v1/auth/refresh":
			_, _ = fmt.Fprint(w, `{"data":{"access_token":"fresh-token","refresh_token":"fresh-refresh"}}`)
		default:
			t.Errorf("balance query must not call pricing: %s", r.URL.Path)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	svc, repo, _ := siteTestService()
	ctx := context.Background()
	site, err := svc.Save(ctx, "", SiteInput{Name: "test", BaseURL: server.URL, Kind: "sub2api", AuthMode: "token", AccessToken: "expired", RefreshToken: "refresh", Enabled: true})
	require.NoError(t, err)
	site, err = svc.RefreshBalance(ctx, site.ID)
	require.NoError(t, err)
	require.Empty(t, site.Balance.Error)
	require.Equal(t, 15.5, *site.Balance.Amount)
	credentials, err := svc.credentials(repo.sites[site.ID])
	require.NoError(t, err)
	require.Equal(t, "fresh-refresh", credentials.RefreshToken)
	require.Equal(t, "fresh-token", credentials.AccessToken)
	raw, err := json.Marshal(site)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "fresh-token")
}

func TestSiteBalanceConcurrentQueriesSendOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":{"id":1,"balance":5}}`)
	}))
	defer server.Close()
	svc, _, _ := siteTestService()
	mailer := &siteBalanceTestMailer{}
	svc.balanceSettings, svc.balanceMailer = balanceTestSettings(), mailer
	site, err := svc.Save(context.Background(), "", SiteInput{Name: "test", BaseURL: server.URL, Kind: "sub2api", AuthMode: "token", AccessToken: "token", Enabled: true})
	require.NoError(t, err)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.RefreshBalance(context.Background(), site.ID)
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	require.Equal(t, 1, mailer.calls)
}

func TestSiteBalanceSettingsValidationAndReadOnlySMTP(t *testing.T) {
	svc, _, _ := siteTestService()
	svc.balanceSettings = balanceTestSettings()
	for _, input := range []SiteBalanceSettings{
		{Enabled: true, Threshold: 0, AdminEmail: "admin@example.com"},
		{Enabled: true, Threshold: -1, AdminEmail: "admin@example.com"},
		{Enabled: true, Threshold: 20, AdminEmail: ""},
		{Enabled: true, Threshold: 20, AdminEmail: "Name <admin@example.com>"},
		{Enabled: true, Threshold: 20, AdminEmail: "admin@example.com\r\nBcc: victim@example.com"},
	} {
		_, err := svc.SaveBalanceSettings(context.Background(), input)
		require.Error(t, err)
	}
	result, err := svc.SaveBalanceSettings(context.Background(), SiteBalanceSettings{Enabled: true, Threshold: 20, AdminEmail: " admin@example.com "})
	require.NoError(t, err)
	require.True(t, result.SMTPConfigured)
	require.Equal(t, "admin@example.com", result.AdminEmail)
}

func TestSiteBalancePollingIndependentOfPriceSchedule(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		require.True(t, strings.HasSuffix(r.URL.Path, "/auth/me"))
		_, _ = fmt.Fprint(w, `{"data":{"id":1,"balance":30}}`)
	}))
	defer server.Close()
	svc, repo, _ := siteTestService()
	svc.balanceSettings = balanceTestSettings()
	site, err := svc.Save(context.Background(), "", SiteInput{Name: "test", BaseURL: server.URL, Kind: "sub2api", AuthMode: "token", AccessToken: "token", Enabled: true})
	require.NoError(t, err)
	future := time.Now().Add(time.Hour)
	repo.sites[site.ID].NextSync = &future
	svc.runDue(context.Background())
	require.NotNil(t, repo.sites[site.ID].Balance)
	require.Equal(t, 30.0, *repo.sites[site.ID].Balance.Amount)
	first := calls
	svc.runDue(context.Background())
	require.Equal(t, first, calls, "balance queries respect their own 5-minute schedule")
}

func TestKongfangBalanceUsesRawCreditsAndDoesNotNeedPricing(t *testing.T) {
	profile := `{"data":{"id":54,"status":"active","balance":221.103,"is_vip":true,"vip_discount":0.5}}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/user/profile", r.URL.Path)
		require.Equal(t, "Bearer console-token", r.Header.Get("Authorization"))
		fmt.Fprint(w, profile)
	}))
	defer server.Close()
	svc, _, _ := siteTestService()
	// A missing conversion must not hide the upstream's credit balance.
	site, err := svc.Save(context.Background(), "", SiteInput{Name: "空凡", Kind: "kongfang", BaseURL: server.URL, AuthMode: "token", AccessToken: "console-token", Enabled: true})
	require.NoError(t, err)
	for _, tc := range []struct {
		value   string
		want    float64
		invalid bool
	}{
		{"221.103", 221.103, false}, {"0", 0, false}, {"-0.25", -0.25, false}, {`"15.5"`, 15.5, false}, {"null", 0, true}, {`"NaN"`, 0, true},
	} {
		profile = fmt.Sprintf(`{"data":{"id":54,"status":"active","balance":%s,"is_vip":true,"vip_discount":0.5}}`, tc.value)
		result, err := svc.RefreshBalance(context.Background(), site.ID)
		require.NoError(t, err)
		if tc.invalid {
			require.NotEmpty(t, result.Balance.Error)
			require.Equal(t, 15.5, *result.Balance.Amount)
			continue
		}
		require.Empty(t, result.Balance.Error)
		require.Equal(t, "积分", result.Balance.Currency)
		require.Equal(t, tc.want, *result.Balance.Amount, "balance is not multiplied by VIP discount")
		require.Nil(t, result.LastSuccess, "balance query does not fetch or publish model prices")
	}
}
