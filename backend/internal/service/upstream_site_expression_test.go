package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUpstreamSiteFixedExpressionPrices(t *testing.T) {
	for _, tc := range []struct {
		name       string
		expression string
		rate       float64
		want       float64
	}{
		{"yh-mo image", `tier("base", fixed(0.04))`, 1, .04},
		{"group multiplier", `tier("base", fixed(0.04))`, 1.5, .06},
		{"integer price", `tier("base", fixed(2))`, .5, 1},
		{"explicit free", `tier("base", fixed(0))`, 2, 0},
		{"zero group multiplier", `tier("base", fixed(0.04))`, 0, 0},
		{"version prefix", `v1: tier("base", fixed(0.05))`, 1, .05},
		{"conditional ceiling", `len <= 32000 ? tier("short", fixed(0.01)) : tier("long", fixed(0.06))`, .5, .03},
		{"request multiplier", `(len <= 32000 ? tier("short", fixed(0.01)) : tier("long", fixed(0.06))) * (hour("UTC") > 8 ? 2 : 1)`, .5, .06},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tier, err := siteExpressionPrices(tc.expression, tc.rate)
			require.NoError(t, err)
			require.Equal(t, "USD/request", tier.Unit)
			require.Len(t, tier.Prices, 1)
			require.Contains(t, tier.Prices, "request")
			require.InDelta(t, tc.want, tier.Prices["request"], 1e-12)
		})
	}
}

func TestUpstreamSiteFixedExpressionMixedBranches(t *testing.T) {
	tier, err := siteExpressionPrices(`len <= 32000 ? tier("short", fixed(0.04)) : tier("long", p*2+c*8)`, 1.5)
	require.NoError(t, err)
	require.Equal(t, "USD/1M tokens", tier.Unit)
	require.Equal(t, map[string]float64{"request": .06, "input_price": 3, "output_price": 12}, tier.Prices,
		"retain both request and token ceilings when different branches use different units")
}

func TestUpstreamSiteFixedExpressionRejectsUnknownPrices(t *testing.T) {
	for _, expression := range []string{
		`fixed(0.04)`,
		`tier("base", fixed())`,
		`tier("base", fixed(0.04, 0.05))`,
		`tier("base", fixed(-0.04))`,
		`tier("base", fixed("0.04"))`,
		`tier("base", fixed(p))`,
		`tier("base", fixed(param("price")))`,
		`tier("base", fixed(0.02+0.02))`,
		`tier("base", fixed(1e308))`,
		`tier("base", fixed(0.04)+p*2)`,
		`tier("base", fixed(0.04)*p)`,
		`len <= 32000 ? tier("short", fixed(0.04)) : tier("long", fixed(param("price")))`,
	} {
		t.Run(expression, func(t *testing.T) {
			_, err := siteExpressionPrices(expression, 1)
			require.Error(t, err)
		})
	}
}
