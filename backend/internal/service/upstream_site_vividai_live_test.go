//go:build unit

package service

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Opt-in, read-only verification of the published contract. No keys, sites,
// accounts, uploads or generation jobs are created, and no database is used.
func TestVividAISiteLiveReadOnly(t *testing.T) {
	key := os.Getenv("SUB2API_VIVIDAI_LIVE_KEY")
	if key == "" {
		t.Skip("SUB2API_VIVIDAI_LIVE_KEY is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	site := &UpstreamSite{Kind: "vividai", BaseURL: "https://vividai.run", CreditUSD: .001}
	adapter := newSiteAdapter(site, &SiteCredentials{AccessToken: key})
	models, err := adapter.catalog(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, models)
	images, videos := 0, 0
	for _, m := range models {
		require.NotNil(t, m.VividAI)
		require.Empty(t, m.Reason)
		if m.Image {
			images++
		} else {
			require.Equal(t, "video", m.VividAI.Kind)
			videos++
		}
	}
	amount, currency, err := adapter.balance(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, amount, 0.0)
	require.Equal(t, "积分", currency)
	require.NotNil(t, site.Concurrency)
	require.Positive(t, site.Concurrency.Limit)
	t.Logf("read-only upstream verification: images=%d videos=%d balance=%g credits concurrency=%d", images, videos, amount, site.Concurrency.Limit)
}
