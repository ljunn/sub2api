//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpstreamSitePersistenceAtomicPolicyAndLocks(t *testing.T) {
	ctx := context.Background()
	repo := NewUpstreamSiteRepository(integrationDB)
	accounts := newAccountRepositoryWithSQL(testEntClient(t), integrationDB, nil)
	bindingID := uuid.NewString()
	account := &service.Account{Name: "site-test", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{service.SiteBindingCredentialKey: bindingID}, Extra: map[string]any{"manual_note": "keep"}}
	require.NoError(t, accounts.Create(ctx, account))
	t.Cleanup(func() { _ = accounts.Delete(ctx, account.ID) })
	now := time.Now().UTC()
	site := &service.UpstreamSite{ID: uuid.NewString(), Name: "site", Secret: "encrypted-only", Enabled: true, LastSuccess: &now, Models: []service.SiteModel{{GroupID: "vip", Model: "image", Tiers: []service.SitePriceTier{{Key: "1K", Unit: "USD/image", Prices: map[string]float64{"request": 0.1}}}}}, Bindings: []service.SiteBinding{{ID: bindingID, GroupID: "vip", Model: "image", AccountID: account.ID, Enabled: true, Limits: []service.SiteTierLimit{{Key: "1K", Unit: "USD/image", Enabled: true, Limits: map[string]float64{"request": 0.2}}}}}}
	t.Cleanup(func() { _ = repo.Delete(ctx, site.ID) })
	require.NoError(t, repo.Save(ctx, site))
	loaded, err := repo.Get(ctx, site.ID)
	require.NoError(t, err)
	assert.Equal(t, site.Secret, loaded.Secret)
	public, err := json.Marshal(loaded)
	require.NoError(t, err)
	assert.NotContains(t, string(public), site.Secret)
	account, err = accounts.GetByID(ctx, account.ID)
	require.NoError(t, err)
	assert.Equal(t, "keep", account.Extra["manual_note"])
	veto, _ := service.SitePriceVeto(service.WithSiteImageSize(ctx, "1K"), account)
	assert.False(t, veto)
	site.Models[0].Tiers[0].Prices["request"] = 0.3
	require.NoError(t, repo.Save(ctx, site))
	require.Error(t, service.CheckSitePriceBeforeSend(service.WithSiteImageSize(ctx, "1K"), account, accounts))
	// Manual prices persist separately and publish to every bound account in the
	// same transaction, while preserving the latest automatic price underneath.
	site.ManualPrices = []service.SiteManualPrice{{GroupID: "vip", Model: "image", BillingMode: "image", Prices: map[string]float64{"1K": 0.05}}}
	require.NoError(t, repo.Save(ctx, site))
	loaded, err = repo.Get(ctx, site.ID)
	require.NoError(t, err)
	require.Equal(t, site.ManualPrices, loaded.ManualPrices)
	require.Equal(t, 0.3, loaded.Models[0].Tiers[0].Prices["request"])
	require.NoError(t, service.CheckSitePriceBeforeSend(service.WithSiteImageSize(ctx, "1K"), account, accounts))
	site.ManualPrices = nil
	require.NoError(t, repo.Save(ctx, site))
	require.Error(t, service.CheckSitePriceBeforeSend(service.WithSiteImageSize(ctx, "1K"), account, accounts))
	var count int
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT count(*) FROM scheduler_outbox WHERE account_id=$1", account.ID).Scan(&count))
	assert.Positive(t, count)
	unlock, err := repo.Lock(ctx, site.ID)
	require.NoError(t, err)
	waiting, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	_, err = repo.Lock(waiting, site.ID)
	assert.Error(t, err)
	unlock()
	unlocked, err := repo.Lock(ctx, site.ID)
	require.NoError(t, err)
	unlocked()
	// The additive migration is safe to run repeatedly and preserves site data.
	require.NoError(t, ApplyMigrations(ctx, integrationDB))
	loaded, err = repo.Get(ctx, site.ID)
	require.NoError(t, err)
	assert.Equal(t, "site", loaded.Name)
}
