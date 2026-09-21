package service

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Explicit opt-in, read-only provider verification. No DB, key creation or
// generation calls; credentials are supplied out of band and never logged.
func TestWuzuLiveReadOnly(t *testing.T) {
	if os.Getenv("WUZU_LIVE_READ_ONLY") != "1" {
		t.Skip("set WUZU_LIVE_READ_ONLY=1 and WUZU_LIVE_URL/USERNAME/PASSWORD to verify the provider")
	}
	base, user, password := os.Getenv("WUZU_LIVE_URL"), os.Getenv("WUZU_LIVE_USERNAME"), os.Getenv("WUZU_LIVE_PASSWORD")
	if base == "" || user == "" || password == "" {
		t.Fatal("missing WUZU read-only verification configuration")
	}
	a := newSiteAdapter(&UpstreamSite{Kind: "wuzu", BaseURL: base, AuthMode: "password", Username: user}, &SiteCredentials{Password: password})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	models, err := a.catalog(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, models)
	_, currency, err := a.balance(ctx)
	require.NoError(t, err)
	require.Equal(t, "额度", currency)
	t.Logf("WUZU login, catalogue and balance verified: %d image configurations; account concurrency %d", len(models), a.site.Concurrency.Limit)
}
