package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamSiteSchedulingUsesCurrentPriceAndRecoversWithoutManualSwitch(t *testing.T) {
	pricing, groups := siteTestPricing()
	a := siteAutomaticAccount(siteAutomaticPolicy())
	ctx := WithSiteImageSize(context.WithValue(context.Background(), sitePricingKey{}, pricing), "2K")
	groups.group.ImagePrice1K, groups.group.ImagePrice2K, groups.group.ImagePrice4K = sitePricePtr(.04), sitePricePtr(.04), sitePricePtr(.04)
	state := pricing.AccountScheduling(ctx, a)
	require.Equal(t, "blocked", state.Status)
	require.Equal(t, "exceeded", state.Reason)
	require.True(t, a.Schedulable, "do not overwrite the manual scheduling preference")
	require.Error(t, CheckSitePriceBeforeSend(ctx, a, nil))
	for _, tier := range state.Tiers {
		require.Equal(t, "exceeded", tier.Status)
		require.Equal(t, .04, tier.Ceiling["request"])
	}
	groups.group.ImagePrice2K = sitePricePtr(.2)
	state = pricing.AccountScheduling(ctx, a)
	require.Equal(t, "partial", state.Status)
	require.NoError(t, CheckSitePriceBeforeSend(ctx, a, nil))
	groups.group.ImagePrice1K, groups.group.ImagePrice4K = sitePricePtr(.2), sitePricePtr(.2)
	require.Equal(t, "ready", pricing.AccountScheduling(ctx, a).Status)
	a.Schedulable = false
	require.Equal(t, "blocked", pricing.AccountScheduling(ctx, a).Status)
	require.Equal(t, "disabled", pricing.AccountScheduling(ctx, a).Reason)
	a.Schedulable = true
	a.Extra["upstream_site_preview_pending"] = true
	a.Status = StatusDisabled
	require.Equal(t, "preview", pricing.AccountScheduling(ctx, a).Reason)
	groups.group.ImagePrice1K, groups.group.ImagePrice2K, groups.group.ImagePrice4K = sitePricePtr(.04), sitePricePtr(.04), sitePricePtr(.04)
	require.Equal(t, "exceeded", pricing.AccountScheduling(ctx, a).Reason, "preview must not hide over-price reasons")
	require.Nil(t, pricing.AccountScheduling(ctx, &Account{ID: 7}), "unmanaged accounts are unaffected")
}

func TestUpstreamSiteManagedNameUsesUpstreamGroupOnSynchronization(t *testing.T) {
	svc, repo, accounts := siteTestService()
	svc.preview = true
	now := time.Now()
	a := siteAutomaticAccount(siteAutomaticPolicy())
	a.Name = "old verbose name"
	a.Status = StatusDisabled
	a.Extra["upstream_site_preview_pending"] = true
	accounts.accounts[a.ID] = a
	site := &UpstreamSite{ID: "site", Name: "非奇异", Enabled: true, LastSuccess: &now,
		Models:   []SiteModel{{GroupID: "up", GroupName: "上游 Adobe", Model: "gpt-image-2", Image: true, Tiers: siteAutomaticPolicy().Tiers}},
		Bindings: []SiteBinding{{ID: "binding", AccountID: a.ID, GroupID: "up", Model: "gpt-image-2", LocalGroupID: 9, LocalModel: "local-alias", Enabled: true}},
	}
	require.NoError(t, repo.Save(context.Background(), site))
	require.NoError(t, svc.updatePolicies(context.Background(), site))
	require.Equal(t, "【非奇异】gpt-image-2（上游 Adobe）", accounts.accounts[a.ID].Name)
	require.Equal(t, StatusDisabled, accounts.accounts[a.ID].Status)
}
