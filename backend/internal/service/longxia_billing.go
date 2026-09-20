package service

import (
	"context"
	"fmt"
)

func (s *OpenAIGatewayService) hasLongXiaPricing(ctx context.Context, model string, key *APIKey) bool {
	if key == nil || key.Group == nil {
		return false
	}
	resolved := s.resolveOpenAIChannelPricing(ctx, model, key)
	return resolved != nil && (resolved.Mode == BillingModePerRequest || resolved.Mode == BillingModeVideo)
}

// Use explicit model pricing only. Neither Ark tokens nor the default xAI
// tariff describes this provider. Billing mode is the operator's resale price,
// independent of which upstream SKU (per-call or PerSecond) was purchased.
func (s *OpenAIGatewayService) calculateLongXiaCost(ctx context.Context, model string, key *APIKey, result *OpenAIForwardResult, multiplier float64) (*CostBreakdown, error) {
	if !s.hasLongXiaPricing(ctx, model, key) {
		return nil, fmt.Errorf("LongXia requires explicit per-request or per-second model pricing")
	}
	if result.VideoCount != 1 || result.VideoDurationSeconds < 3 || result.VideoDurationSeconds > 25 {
		return nil, fmt.Errorf("LongXia billing units are missing or invalid")
	}
	resolved := s.resolveOpenAIChannelPricing(ctx, model, key)
	units := float64(result.VideoCount)
	if resolved.Mode == BillingModeVideo {
		units *= float64(result.VideoDurationSeconds)
	}
	return s.billingService.CalculateCostUnified(CostInput{
		Ctx: ctx, Model: model, GroupID: &key.Group.ID, Group: key.Group,
		RequestCount: result.VideoCount, UsageUnits: units, SizeTier: result.VideoResolution,
		RateMultiplier: multiplier, Resolver: s.resolver, Resolved: resolved,
	})
}
