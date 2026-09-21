package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestUpstreamSiteEqualPriceDefaultsOffAndUsesLatestGroupOnEverySend(t *testing.T) {
	pricing, groups := siteTestPricing()
	groups.group.ImagePrice1K = sitePricePtr(.1)
	policy := pricing.apply(context.Background(), siteAutomaticPolicy(), false)
	account := siteAutomaticAccount(policy)
	ctx := WithSiteImageSize(context.WithValue(context.Background(), sitePricingKey{}, pricing), "1K")
	require.Equal(t, "site_price_equal", siteTierReason(policy, "1K", time.Now()))
	require.Error(t, CheckSitePriceBeforeSend(ctx, account, nil))
	require.Equal(t, "equal", pricing.AccountScheduling(ctx, account).Tiers[0].Status)
	// A queued account still carries the old policy. Read the live group setting
	// at every attempt so enabling and disabling do not require an upstream sync.
	groups.group.AllowEqualPriceScheduling = true
	require.NoError(t, CheckSitePriceBeforeSend(ctx, account, nil))
	require.Equal(t, "ready", pricing.AccountScheduling(ctx, account).Tiers[0].Status)
	groups.group.AllowEqualPriceScheduling = false
	require.Error(t, CheckSitePriceBeforeSend(ctx, account, nil))
	// One equal resolution cannot block another resolution with lower cost.
	require.NoError(t, CheckSitePriceBeforeSend(WithSiteImageSize(ctx, "2K"), account, nil))
	require.Error(t, CheckSitePriceBeforeSend(WithSiteImageSize(ctx, "auto"), account, nil))
	// Do not carry the previous group's opt-in to another bound group.
	groups.group = &Group{ID: 10, Status: StatusActive, Platform: PlatformOpenAI, RateMultiplier: 1, ImagePrice1K: sitePricePtr(.1)}
	p := siteAutomaticPolicy()
	p.LocalGroupID = 10
	require.Equal(t, "site_price_equal", siteTierReason(pricing.apply(ctx, p, false), "1K", time.Now()))
}

func TestUpstreamSiteEqualPriceComparisonAndFreeComponents(t *testing.T) {
	for _, tc := range []struct {
		name            string
		prices, ceiling map[string]float64
		allow           bool
		want            string
	}{
		{"equal default", map[string]float64{"request": .04}, map[string]float64{"request": .04}, false, "site_price_equal"},
		{"equal enabled", map[string]float64{"request": .04}, map[string]float64{"request": .04}, true, ""},
		{"lower", map[string]float64{"request": .039}, map[string]float64{"request": .04}, false, ""},
		{"higher enabled", map[string]float64{"request": .041}, map[string]float64{"request": .04}, true, "site_price_exceeded"},
		{"rounding", map[string]float64{"request": .3}, map[string]float64{"request": .1 + .2}, false, "site_price_equal"},
		{"free optional cache", map[string]float64{"input_price": 1, "cache_write_price": 0}, map[string]float64{"input_price": 2, "cache_write_price": 0}, false, ""},
		{"one paid component equal", map[string]float64{"input_price": 1, "output_price": 3}, map[string]float64{"input_price": 2, "output_price": 3}, false, "site_price_equal"},
		{"all free", map[string]float64{"request": 0}, map[string]float64{"request": 0}, false, "site_price_equal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := SiteAccountPolicy{Enabled: true, FreshUntil: time.Now().Add(time.Minute), Tiers: []SitePriceTier{{Key: "default", Unit: "USD/request", Prices: tc.prices}}, Limits: []SiteTierLimit{{Key: "default", Unit: "USD/request", Enabled: true, Limits: tc.ceiling, AllowEqualPriceScheduling: tc.allow}}}
			require.Equal(t, tc.want, siteTierReason(p, "default", time.Now()))
		})
	}
}
