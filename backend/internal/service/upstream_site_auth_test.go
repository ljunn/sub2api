//go:build unit

package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamSiteNewAPIRefreshOriginRecoveryPersistsAndReusesSession(t *testing.T) {
	const canonical = "https://dashboard.example.com"
	refreshCalls, loginCalls, statusCalls := 0, 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/self":
			if r.Header.Get("Authorization") != "Bearer renewed" {
				w.WriteHeader(http.StatusUnauthorized)
				writeSiteJSON(w, map[string]any{"success": false, "code": "AUTH_TOKEN_EXPIRED"})
				return
			}
			writeSiteJSON(w, map[string]any{"success": true, "data": map[string]any{"id": 12}})
		case "/api/user/auth/refresh":
			refreshCalls++
			cookie, err := r.Cookie("new_api_refresh")
			require.NoError(t, err)
			require.Equal(t, "existing-refresh", cookie.Value)
			if r.Header.Get("Origin") != canonical {
				w.WriteHeader(http.StatusForbidden)
				writeSiteJSON(w, map[string]any{"success": false, "code": "AUTH_ORIGIN_FORBIDDEN"})
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "new_api_refresh", Value: "rotated-refresh", Path: "/"})
			writeSiteJSON(w, map[string]any{"success": true, "data": map[string]any{"access_token": "renewed", "user": map[string]int{"id": 12}}})
		case "/api/status":
			statusCalls++
			writeSiteJSON(w, map[string]any{"success": true, "data": map[string]string{"server_address": canonical + "/"}})
		case "/api/user/login", "/api/user/login/encryption-key":
			loginCalls++
			w.WriteHeader(http.StatusConflict)
			writeSiteJSON(w, map[string]any{"success": false, "code": "AUTH_SESSION_LIMIT"})
		case "/api/pricing":
			// A catalogue failure must not discard the successfully rotated tokens.
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	svc, repo, _ := siteTestService()
	ctx := context.Background()
	site, err := svc.Save(ctx, "", SiteInput{Name: "alternate endpoint", BaseURL: server.URL, Kind: "newapi", AuthMode: "password", Username: "operator", Password: "password", Enabled: true})
	require.NoError(t, err)
	require.NoError(t, svc.saveSecret(ctx, site, &SiteCredentials{Password: "password", AccessToken: "expired", RefreshToken: "existing-refresh"}))
	for i := 0; i < 3; i++ {
		result, err := svc.Sync(ctx, site.ID)
		require.NoError(t, err)
		require.Contains(t, result.Error, "HTTP 503")
		credentials, err := svc.credentials(repo.sites[site.ID])
		require.NoError(t, err)
		require.Equal(t, "renewed", credentials.AccessToken)
		require.Equal(t, "rotated-refresh", credentials.RefreshToken)
	}
	require.Equal(t, 0, loginCalls)
	require.Equal(t, 2, refreshCalls, "one rejected Origin and one successful refresh of the same session")
	require.Equal(t, 1, statusCalls)
}

func TestUpstreamSiteNewAPIAuthFailuresDoNotCreateSessions(t *testing.T) {
	for _, tc := range []struct {
		name, code, canonical string
		profile               bool
		status                int
	}{
		{name: "profile forbidden", profile: true, status: 403},
		{name: "refresh forbidden", status: 403},
		{name: "origin forbidden without canonical address", code: "AUTH_ORIGIN_FORBIDDEN", status: 403},
		{name: "origin forbidden with invalid canonical address", code: "AUTH_ORIGIN_FORBIDDEN", canonical: "https://user:secret@example.com", status: 403},
		{name: "canonical origin also forbidden", code: "AUTH_ORIGIN_FORBIDDEN", canonical: "https://dashboard.example.com", status: 403},
		{name: "session mismatch", code: "AUTH_SESSION_MISMATCH", status: 409},
		{name: "refresh race", code: "AUTH_REFRESH_RACE", status: 409},
		{name: "rate limited", code: "AUTH_SESSION_ISSUANCE_LIMIT", status: 429},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loginCalls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/user/self", "/api/user/auth/refresh":
					w.WriteHeader(tc.status)
					writeSiteJSON(w, map[string]any{"success": false, "code": tc.code})
				case "/api/status":
					writeSiteJSON(w, map[string]any{"success": true, "data": map[string]string{"server_address": tc.canonical}})
				default:
					loginCalls++
					w.WriteHeader(http.StatusConflict)
				}
			}))
			defer server.Close()
			credentials := &SiteCredentials{Password: "password", RefreshToken: "existing-refresh"}
			if tc.profile {
				credentials.AccessToken = "existing-access"
			}
			a := newSiteAdapter(&UpstreamSite{BaseURL: server.URL, Kind: "newapi", AuthMode: "password", Username: "operator"}, credentials)
			err := a.authenticate(context.Background())
			var remote *siteRemoteError
			require.ErrorAs(t, err, &remote)
			require.Equal(t, tc.status, remote.Status)
			require.Equal(t, tc.code, remote.Code)
			require.Equal(t, 0, loginCalls)
			require.Equal(t, "existing-refresh", credentials.RefreshToken)
		})
	}
}

func TestUpstreamSiteNewAPILoginSessionLimitIsActionableAndRedacted(t *testing.T) {
	for _, code := range []string{"AUTH_SESSION_LIMIT", "unknown-secret-code"} {
		t.Run(code, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/user/login/encryption-key" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(http.StatusConflict)
				writeSiteJSON(w, map[string]any{"success": false, "code": code, "message": "password=secret-password access_token=secret-access"})
			}))
			defer server.Close()
			a := newSiteAdapter(&UpstreamSite{BaseURL: server.URL, Kind: "newapi", AuthMode: "password", Username: "operator"}, &SiteCredentials{Password: "password"})
			err := a.authenticate(context.Background())
			require.Error(t, err)
			require.Contains(t, err.Error(), "/api/user/login")
			require.Contains(t, err.Error(), "HTTP 409")
			require.NotContains(t, err.Error(), "secret")
			if code == "AUTH_SESSION_LIMIT" {
				require.Contains(t, err.Error(), code)
				require.Contains(t, err.Error(), "登录会话数量已达上限")
			}
		})
	}
}

func TestUpstreamSiteCanonicalAuthOrigin(t *testing.T) {
	for _, raw := range []string{"http://example.com", "https://user:secret@example.com", "https://example.com/path", "https://example.com?token=secret", "https://example.com?", "https://example.com#secret", "//example.com", "not a URL"} {
		t.Run(fmt.Sprintf("reject %s", raw), func(t *testing.T) {
			_, ok := siteCanonicalAuthOrigin(raw)
			require.False(t, ok)
		})
	}
	origin, ok := siteCanonicalAuthOrigin(" https://dashboard.example.com:8443/ ")
	require.True(t, ok)
	require.Equal(t, "https://dashboard.example.com:8443", origin)
}
