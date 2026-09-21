//go:build unit

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type schedulerSitePricingGroups struct {
	service.GroupRepository
	group *service.Group
}

func (r schedulerSitePricingGroups) GetByIDLite(context.Context, int64) (*service.Group, error) {
	return r.group, nil
}

func TestSchedulerCacheSnapshotPreservesSitePricingAndUpdates(t *testing.T) {
	ctx := context.Background()
	cache := newSchedulerCacheUnit(t)
	const model = "gemini-3-pro-image-preview"
	policy := service.SiteAccountPolicy{
		BindingID: "binding", SiteKind: "kongfang", LocalGroupID: 11,
		LocalModel: model, UpstreamModel: model, Image: true, Enabled: true,
		FreshUntil: time.Now().Add(time.Hour).UTC(),
		Tiers:      []service.SitePriceTier{{Key: "2K", Unit: "USD/image", Prices: map[string]float64{"request": .03}}},
		Limits:     []service.SiteTierLimit{{Key: "2K", Unit: "USD/image", Enabled: true}},
	}
	account := service.Account{
		ID: 32, Platform: service.PlatformGemini, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Priority: 50, GroupIDs: []int64{11},
		Credentials: map[string]any{service.SiteBindingCredentialKey: "binding", "model_mapping": map[string]any{model: model}},
		Extra:       map[string]any{service.SiteBindingCredentialKey: "binding", service.SitePolicyExtraKey: policy},
	}
	bucket := service.SchedulerBucket{GroupID: 11, Platform: service.PlatformGemini, Mode: service.SchedulerModeMixed}
	token, err := cache.CaptureBucketWriteToken(ctx, bucket)
	require.NoError(t, err)
	require.NoError(t, cache.SetSnapshot(ctx, bucket, token, []service.Account{account}))

	retail := .08
	groups := schedulerSitePricingGroups{group: &service.Group{
		ID: 11, Status: service.StatusActive, Platform: service.PlatformGemini,
		RateMultiplier: 1, ImagePrice2K: &retail,
	}}
	billing := service.NewBillingService(&config.Config{}, nil)
	pricing := service.NewUpstreamSitePricing(groups, billing, service.NewModelPricingResolver(nil, billing), nil)
	read := func() *service.Account {
		t.Helper()
		accounts, hit, err := cache.GetSnapshot(ctx, bucket)
		require.NoError(t, err)
		require.True(t, hit)
		require.Len(t, accounts, 1)
		return accounts[0]
	}
	cached := read()
	restored, managed := cached.SitePolicy()
	require.True(t, managed, "cached candidates must participate in site priority scoring")
	require.Equal(t, policy, restored, "both the binding credential and complete policy must survive the snapshot")
	scheduling := pricing.AccountScheduling(ctx, cached)
	require.Equal(t, "ready", scheduling.Status)
	require.NotNil(t, scheduling.Tiers[0].PriorityScore)
	require.InDelta(t, .03/.08, scheduling.Tiers[0].PriorityScore.CostRatio, 1e-9)

	// A price refresh updates existing snapshot members without rebuilding the bucket.
	policy.Tiers[0].Prices["request"] = .09
	account.Extra[service.SitePolicyExtraKey] = policy
	require.NoError(t, cache.SetAccount(ctx, &account))
	scheduling = pricing.AccountScheduling(ctx, read())
	require.Equal(t, "blocked", scheduling.Status)
	require.Equal(t, "exceeded", scheduling.Reason)
	require.Nil(t, scheduling.Tiers[0].PriorityScore)
}

func TestSchedulerMetadataInvalidSitePolicyStaysManaged(t *testing.T) {
	account := service.Account{
		Credentials: map[string]any{service.SiteBindingCredentialKey: "expected"},
		Extra:       map[string]any{service.SitePolicyExtraKey: map[string]any{"binding_id": "different"}},
	}
	payload, err := json.Marshal(buildSchedulerMetadataAccount(account))
	require.NoError(t, err)
	cached, err := decodeCachedAccount(payload)
	require.NoError(t, err)
	_, managed := cached.SitePolicy()
	require.True(t, managed, "invalid site metadata must not turn into an unrestricted independent account")
	veto, _ := service.SitePriceVeto(context.Background(), cached)
	require.True(t, veto)
}
