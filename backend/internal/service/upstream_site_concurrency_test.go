package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamSiteConcurrencyProfiles(t *testing.T) {
	for _, tc := range []struct {
		name, kind, profile, source string
		want                        int
		invalid                     bool
	}{
		{"sub2api published", "sub2api", `{"id":7,"concurrency":5000}`, "concurrency", 5000, false},
		{"sub2api limited", "sub2api", `{"id":7,"concurrency":150}`, "concurrency", 150, false},
		{"newapi unpublished", "newapi", `{"id":7,"rpm_limit":2}`, "default", 1000, false},
		{"unlimited", "sub2api", `{"id":7,"concurrency":0}`, "default", 1000, false},
		{"kongfang effective override", "kongfang", `{"id":7,"effective_max_concurrency":1000,"max_concurrency":5}`, "effective_max_concurrency", 1000, false},
		{"kongfang configured fallback", "kongfang", `{"id":7,"max_concurrency":40}`, "max_concurrency", 40, false},
		{"newapi extension", "newapi", `{"id":7,"max_concurrency":20}`, "max_concurrency", 20, false},
		{"invalid response", "sub2api", `[]`, "", 0, true},
		{"invalid limit", "sub2api", `{"id":7,"concurrency":"unknown"}`, "", 0, true},
		{"fractional limit", "sub2api", `{"id":7,"concurrency":1.5}`, "", 0, true},
		{"negative limit", "sub2api", `{"id":7,"concurrency":-1}`, "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				require.Equal(t, http.MethodGet, r.Method)
				require.Equal(t, "Bearer saved-token", r.Header.Get("Authorization"))
				fmt.Fprintf(w, `{"code":0,"data":%s}`, tc.profile)
			}))
			defer server.Close()
			previous := &SiteConcurrency{Limit: 80, Source: "concurrency"}
			site := &UpstreamSite{Kind: tc.kind, BaseURL: server.URL, UserID: 7, Concurrency: previous}
			a := newSiteAdapter(site, &SiteCredentials{AccessToken: "saved-token"})
			err := a.authenticate(context.Background())
			if tc.invalid {
				require.Error(t, err)
				require.Same(t, previous, site.Concurrency, "invalid responses must not erase a known cap")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, site.Concurrency.Limit)
			require.Equal(t, tc.source, site.Concurrency.Source)
			require.WithinDuration(t, time.Now(), site.Concurrency.CheckedAt, time.Second)
		})
	}
}

func TestUpstreamSiteConcurrencyAfterTokenRefresh(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/auth/refresh":
			fmt.Fprint(w, `{"code":0,"data":{"access_token":"renewed"}}`)
		case "/api/v1/auth/me":
			if r.Header.Get("Authorization") != "Bearer renewed" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"code":0,"data":{"id":7,"concurrency":150}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	site := &UpstreamSite{Kind: "sub2api", BaseURL: server.URL, UserID: 7}
	a := newSiteAdapter(site, &SiteCredentials{AccessToken: "expired", RefreshToken: "refresh"})
	require.NoError(t, a.authenticate(context.Background()))
	require.Equal(t, 150, site.Concurrency.Limit)
}

func TestUpstreamSiteConcurrencySynchronizationPreservesScheduling(t *testing.T) {
	svc, repo, accounts := siteTestService()
	a := siteAutomaticAccount(siteAutomaticPolicy())
	a.Concurrency, a.Schedulable = 1, false
	accounts.accounts[a.ID] = a
	accounts.accounts[100] = &Account{ID: 100, Concurrency: 33}
	now := time.Now()
	site := &UpstreamSite{ID: "site", Name: "site", Enabled: true, LastSuccess: &now,
		Concurrency: &SiteConcurrency{Limit: 500, Source: "concurrency"},
		Models:      []SiteModel{{GroupID: "up", Model: "gpt-image-2", Image: true, Tiers: siteAutomaticPolicy().Tiers}},
		Bindings:    []SiteBinding{{ID: "binding", AccountID: a.ID, GroupID: "up", Model: "gpt-image-2", LocalGroupID: 9, LocalModel: "local-alias", Enabled: true}},
	}
	require.NoError(t, repo.Save(context.Background(), site))
	for _, limit := range []int{500, 150, 1000} {
		site.Concurrency.Limit = limit
		require.NoError(t, svc.updatePolicies(context.Background(), site))
		require.Equal(t, limit, accounts.accounts[a.ID].Concurrency)
		require.False(t, accounts.accounts[a.ID].Schedulable)
		require.Equal(t, 33, accounts.accounts[100].Concurrency, "standalone accounts stay unchanged")
	}
	site.Concurrency = nil
	accounts.accounts[a.ID].Concurrency = 150
	require.NoError(t, svc.updatePolicies(context.Background(), site))
	require.Equal(t, 150, accounts.accounts[a.ID].Concurrency, "no successful profile read must not lift an existing cap")
}
