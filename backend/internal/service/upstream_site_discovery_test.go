package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamSiteDiscoveryBaselineAndSuccessfulChanges(t *testing.T) {
	now := time.Now().UTC()
	site := &UpstreamSite{ID: "site"}
	base := []SiteModel{{GroupID: "one", Model: "existing"}}
	trackSiteModelDiscoveries(site, base, now)
	require.Empty(t, base[0].DiscoveryID, "initial catalogue is a baseline")
	site.Models, site.LastSuccess = base, &now
	updated := []SiteModel{{GroupID: "one", Model: "existing"}, {GroupID: "one", Model: "new"}, {GroupID: "two", Model: "existing"}}
	trackSiteModelDiscoveries(site, updated, now)
	require.Empty(t, updated[0].DiscoveryID)
	require.NotEmpty(t, updated[1].DiscoveryID)
	require.NotEqual(t, updated[1].DiscoveryID, updated[2].DiscoveryID)
	firstID := updated[1].DiscoveryID
	// A repeated scan or a cleared price cache must retain the same discovery.
	site.Models, site.LastSuccess = nil, nil
	trackSiteModelDiscoveries(site, updated, now.Add(time.Minute))
	require.Equal(t, firstID, updated[1].DiscoveryID)
	require.Equal(t, now, *updated[1].DiscoveredAt)
	trackSiteModelDiscoveries(site, base, now.Add(2*time.Minute))
	trackSiteModelDiscoveries(site, updated, now.Add(3*time.Minute))
	require.NotEqual(t, firstID, updated[1].DiscoveryID, "a removed model returning is a fresh discovery")
	// Upgrade uses an existing site's successful catalogue without flagging it all.
	legacy := &UpstreamSite{Models: base, LastSuccess: &now}
	trackSiteModelDiscoveries(legacy, updated, now)
	require.Empty(t, updated[0].DiscoveryID)
	require.NotEmpty(t, updated[1].DiscoveryID)
}

func TestUpstreamSiteDiscoveryReadPersistsPerAdminWithoutAcknowledgingConcurrentArrivals(t *testing.T) {
	svc, repo, accounts := siteTestService()
	svc.balanceSettings = &siteBalanceSettingRepo{values: map[string]string{}}
	ctx := context.Background()
	site := &UpstreamSite{ID: "site", ModelCatalogue: &SiteModelCatalogue{Entries: map[string]SiteModelDiscovery{}}}
	models := []SiteModel{{GroupID: "g", Model: "first"}, {GroupID: "g", Model: "second"}}
	trackSiteModelDiscoveries(site, models, time.Now())
	site.Models = models
	require.NoError(t, repo.Save(ctx, site))
	first := models[0].DiscoveryID
	second := models[1].DiscoveryID
	ids, err := svc.MarkModelsRead(ctx, site.ID, 1, []string{first, "unknown", first})
	require.NoError(t, err)
	require.Equal(t, []string{first}, ids)
	list, _ := svc.List(ctx)
	require.NoError(t, svc.AnnotateUnreadModels(ctx, list, 1))
	require.False(t, list[0].Models[0].Unread)
	require.True(t, list[0].Models[1].Unread, "a model not actually viewed stays unread")
	require.Nil(t, list[0].ModelCatalogue)
	// Read-only listing never marks anything read and does not alter stored metadata.
	list, _ = svc.List(ctx)
	require.NoError(t, svc.AnnotateUnreadModels(ctx, list, 2))
	require.True(t, list[0].Models[0].Unread)
	require.True(t, list[0].Models[1].Unread)
	stored, _ := repo.Get(ctx, site.ID)
	require.NotNil(t, stored.ModelCatalogue)
	raw, _ := json.Marshal(stored)
	require.NotContains(t, string(raw), `"unread":true`)
	// A second tab merges read receipts; it cannot reset the first tab's receipt.
	_, err = svc.MarkModelsRead(ctx, site.ID, 1, []string{second})
	require.NoError(t, err)
	list, _ = svc.List(ctx)
	require.NoError(t, svc.AnnotateUnreadModels(ctx, list, 1))
	require.False(t, list[0].Models[0].Unread)
	require.False(t, list[0].Models[1].Unread)
	require.Zero(t, accounts.creates, "viewing never binds or provisions an account")
	_, err = svc.MarkModelsRead(ctx, site.ID, 0, []string{first})
	require.Error(t, err)
	_, err = svc.MarkModelsRead(ctx, site.ID, 1, make([]string, 101))
	require.Error(t, err)
}
