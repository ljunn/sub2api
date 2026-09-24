//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGroupPricingRollingVersionCompatibility(t *testing.T) {
	var pricing ChannelModelPricing
	require.NoError(t, json.Unmarshal([]byte(`{"models":["custom"],"max_reasoning_effort_multiplier":3}`), &pricing))
	require.Equal(t, map[string]float64{"max": 3}, pricing.ReasoningEffortMultipliers)
	pricing.ReasoningEffortMultipliers = map[string]float64{"max": 4, "high": 2}
	out, err := json.Marshal(pricing)
	require.NoError(t, err)
	var old struct {
		Max *float64 `json:"max_reasoning_effort_multiplier"`
	}
	require.NoError(t, json.Unmarshal(out, &old))
	require.NotNil(t, old.Max)
	require.Equal(t, 4.0, *old.Max)
	for _, generic := range []string{`{}`, `null`, `{"high":2}`} {
		require.NoError(t, json.Unmarshal([]byte(`{"max_reasoning_effort_multiplier":3,"reasoning_effort_multipliers":`+generic+`}`), &pricing))
		require.NotContains(t, pricing.ReasoningEffortMultipliers, "max", "explicit generic settings must not revive legacy max")
		out, err = json.Marshal(pricing)
		require.NoError(t, err)
		require.NotContains(t, string(out), "max_reasoning_effort_multiplier")
	}
}
