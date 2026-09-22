package service

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
)

// UpstreamSitePricing uses the same group/model price resolution as billing.
// Saved limits are display snapshots only; admission always resolves local prices.
type UpstreamSitePricing struct {
	groups        GroupRepository
	billing       *BillingService
	resolver      *ModelPricingResolver
	rates         UserGroupRateRepository
	performance   sitePerformanceStore
	accounts      AccountRepository
	priorityViews sitePriorityViewCache
	traffic       siteTrafficStore
	support       siteSupportSettings
}

func NewUpstreamSitePricing(groups GroupRepository, billing *BillingService, resolver *ModelPricingResolver, rates UserGroupRateRepository) *UpstreamSitePricing {
	return &UpstreamSitePricing{groups: groups, billing: billing, resolver: resolver, rates: rates}
}

type sitePricingKey struct{}

func (s *UpstreamSiteService) WithPricingContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, sitePricingKey{}, s.pricing)
}

func siteComparisonTiers(image bool, tiers []SitePriceTier) []SitePriceTier {
	if image && len(tiers) == 1 && tiers[0].Key == "default" && (tiers[0].Unit == "USD/request" || tiers[0].Unit == "USD/image") {
		out := make([]SitePriceTier, 0, 3)
		for _, key := range []string{"1K", "2K", "4K"} {
			t := tiers[0]
			t.Key = key
			out = append(out, t)
		}
		return out
	}
	return tiers
}

func (s *UpstreamSitePricing) apply(ctx context.Context, p SiteAccountPolicy, request bool) SiteAccountPolicy {
	p.Tiers = vividAIPerSecondTiers(p.VividAI, p.Tiers)
	p.Tiers = siteComparisonTiers(p.Image, p.Tiers)
	old := p.Limits
	p.Limits = make([]SiteTierLimit, 0, len(p.Tiers))
	var group *Group
	var err error
	if s == nil || s.groups == nil {
		err = errors.New("local pricing unavailable")
	} else {
		group, err = s.groups.GetByIDLite(ctx, p.LocalGroupID)
	}
	var authGroup *Group
	if request {
		authGroup, _ = ctx.Value(ctxkey.Group).(*Group)
	}
	billingGroup := group
	if authGroup != nil && authGroup.ID != p.LocalGroupID && s != nil && s.groups != nil {
		billingGroup, err = s.groups.GetByIDLite(ctx, authGroup.ID)
	}
	var selling map[string]map[string]float64
	if err == nil && group != nil && billingGroup != nil && group.Status == StatusActive && billingGroup.Status == StatusActive {
		model := p.LocalModel
		if req, ok := ctx.Value(siteRequestKey{}).(SitePriceRequest); request && ok && req.Model != "" {
			model = req.Model
		}
		selling, err = s.selling(ctx, billingGroup, model, p.Image, p.Tiers, request)
		// Billing can still carry an authentication snapshot during a concurrent edit.
		// Use the lower price until that snapshot also sees the new configuration.
		if err == nil && authGroup != nil {
			previous, e := s.selling(ctx, authGroup, model, p.Image, p.Tiers, request)
			if e != nil {
				err = e
			} else {
				siteMinPrices(selling, previous)
			}
		}
	} else {
		err = errors.New("local group unavailable")
	}
	factor := 1.0
	if group != nil && group.ProfitControlEnabled {
		factor = clampProfitControlThreshold(1 - group.ProfitMinMargin - group.ProfitSafetyBuffer)
	}
	if authGroup != nil && authGroup.ID == p.LocalGroupID && authGroup.ProfitControlEnabled {
		factor = math.Min(factor, clampProfitControlThreshold(1-authGroup.ProfitMinMargin-authGroup.ProfitSafetyBuffer))
	}
	for _, tier := range p.Tiers {
		limit := SiteTierLimit{AllowEqualPriceScheduling: group != nil && group.AllowEqualPriceScheduling, Key: tier.Key, Unit: tier.Unit, Enabled: true, Limits: map[string]float64{}, Selling: map[string]float64{}}
		for _, prior := range old {
			if prior.Key == tier.Key || prior.Key == "default" {
				limit.Enabled = prior.Enabled
				if prior.Key == tier.Key {
					break
				}
			}
		}
		if err != nil {
			limit.Reason = "本地分组或模型售价不可用，请检查分组定价"
		} else {
			for component := range tier.Prices {
				v, ok := selling[tier.Key][component]
				if !ok || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
					limit.Reason = "本地定价未覆盖此计费项目或单位"
					continue
				}
				limit.Selling[component] = v
				limit.Limits[component] = v * factor
			}
		}
		p.Limits = append(p.Limits, limit)
	}
	// Retain disabled resolutions if the upstream temporarily removes them.
	// They must not silently re-enable when the catalogue recovers.
	for _, prior := range old {
		if prior.Enabled {
			continue
		}
		found := false
		for _, current := range p.Limits {
			if current.Key == prior.Key {
				found = true
				break
			}
		}
		if !found && prior.Key != "default" {
			p.Limits = append(p.Limits, SiteTierLimit{Key: prior.Key, Unit: prior.Unit, Enabled: false, Limits: map[string]float64{}, Reason: "上游暂未提供此档位价格"})
		}
	}
	return p
}
func siteMinPrices(prices, other map[string]map[string]float64) {
	for tier, components := range prices {
		for key, v := range components {
			if prior, ok := other[tier][key]; ok {
				components[key] = math.Min(v, prior)
			} else {
				delete(components, key)
			}
		}
	}
}
func (s *UpstreamSitePricing) selling(ctx context.Context, group *Group, model string, image bool, tiers []SitePriceTier, request bool) (map[string]map[string]float64, error) {
	if s.billing == nil || s.resolver == nil {
		return nil, ErrModelPricingUnavailable
	}
	ctx = WithResolvedTargetPlatform(ctx, group.Platform)
	at, ok := openAIPricingAtFromContext(ctx)
	if !ok {
		at, ok = gatewayTokenRequestPricingAtFromContext(ctx)
		if !ok {
			at = timezone.Now()
		}
	}
	perImage := image
	for _, tier := range tiers {
		if tier.Unit == "USD/1M tokens" {
			perImage = false
			break
		}
	}
	resolved := s.resolver.Resolve(ctx, PricingInput{Model: model, GroupID: &group.ID, Group: group})
	perVideo := false
	for _, tier := range tiers {
		perVideo = perVideo || (tier.Unit == "USD/second" && resolved != nil && resolved.Mode == BillingModeVideo)
	}
	rate := group.RateMultiplier
	if request && !(perImage && group.ImageRateIndependent) && !(perVideo && group.VideoRateIndependent) {
		if userID, _ := ctx.Value(ctxkey.UserID).(int64); userID > 0 {
			if s.rates == nil {
				return nil, errors.New("user pricing unavailable")
			}
			override, err := s.rates.GetByUserAndGroup(ctx, userID, group.ID)
			if err != nil {
				return nil, err
			}
			if override != nil {
				rate = *override
			}
		}
	}
	if perImage {
		rate = resolveImageRateMultiplier(&APIKey{Group: group}, rate)
	} else if perVideo {
		rate = resolveVideoRateMultiplier(&APIKey{Group: group}, rate)
	} else {
		rate *= group.PeakMultiplierAt(at)
	}
	if rate < 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return nil, ErrModelPricingUnavailable
	}
	out := map[string]map[string]float64{}
	var schedule *ContextPricingSchedule
	var scheduleErr error
	for _, tier := range tiers {
		prices := map[string]float64{}
		out[tier.Key] = prices
		if tier.Unit == "USD/second" {
			if resolved == nil || (resolved.Source != PricingSourceGroup && resolved.Source != PricingSourceChannel) {
				continue
			}
			switch resolved.Mode {
			case BillingModeVideo:
				cost, e := s.billing.CalculateCostUnified(CostInput{Ctx: ctx, Model: model, Group: group, GroupID: &group.ID, UsageUnits: 1, SizeTier: tier.Key, RateMultiplier: rate, Resolver: s.resolver, Resolved: resolved})
				if e == nil && cost != nil {
					prices["second"] = cost.ActualCost
				}
			case BillingModePerRequest:
				if tier.MaxDurationSeconds > 0 {
					v := resolved.DefaultPerRequestPrice
					for _, t := range resolved.RequestTiers {
						if t.PerRequestPrice != nil {
							v = math.Min(v, *t.PerRequestPrice)
						}
					}
					prices["second"] = v * rate / float64(tier.MaxDurationSeconds)
				}
			case BillingModeToken:
				videoSchedule, e := s.billing.ResolveContextPricingSchedule(ctx, s.resolver, ContextPricingScheduleInput{Model: model, Group: group, Platform: group.Platform})
				if tier.CreditUnits > 0 && e == nil && videoSchedule != nil && len(videoSchedule.Tiers) > 0 {
					unit := math.Inf(1)
					for _, t := range videoSchedule.Tiers {
						v := 0.0
						if t.Output != nil {
							v = *t.Output
						}
						unit = math.Min(unit, v)
					}
					prices["second"] = unit * tier.CreditUnits * rate * resolvedChannelTimeMultiplier(resolved, at)
				}
			}
			continue
		}
		if tier.Unit == "USD/request" || tier.Unit == "USD/image" {
			if image {
				// A local token/video card cannot be compared with a per-image
				// purchase price without knowing usage. Do not use image fallback.
				if resolved != nil && (resolved.Source == PricingSourceGroup || resolved.Source == PricingSourceChannel) && resolved.Mode != BillingModeImage && resolved.Mode != BillingModePerRequest {
					continue
				}
				// Match calculateOpenAIImageCost: group model card, group resolution,
				// channel model card, then the standard image fallback.
				var cost *CostBreakdown
				configured := resolved != nil && (resolved.Mode == BillingModeImage || resolved.Mode == BillingModePerRequest)
				groupPrice := map[string]*float64{"1K": group.ImagePrice1K, "2K": group.ImagePrice2K, "4K": group.ImagePrice4K}[tier.Key]
				if configured && (resolved.Source == PricingSourceGroup || groupPrice == nil) {
					cost, _ = s.billing.CalculateCostUnified(CostInput{Ctx: ctx, Model: model, Group: group, GroupID: &group.ID, RequestCount: 1, SizeTier: tier.Key, RateMultiplier: rate, Resolver: s.resolver, Resolved: resolved})
				}
				if cost == nil {
					cost = s.billing.CalculateImageCost(model, tier.Key, 1, &ImagePriceConfig{Price1K: group.ImagePrice1K, Price2K: group.ImagePrice2K, Price4K: group.ImagePrice4K}, rate)
				}
				prices["request"] = cost.ActualCost
			} else if tier.CreditUnits > 0 && resolved != nil && resolved.Mode == BillingModeToken && (resolved.Source == PricingSourceGroup || resolved.Source == PricingSourceChannel) {
				// VividAI output is credit-equivalent usage, never native Ark tokens.
				// Require explicit local pricing and use the lowest context unit rate.
				videoSchedule, e := s.billing.ResolveContextPricingSchedule(ctx, s.resolver, ContextPricingScheduleInput{Model: model, Group: group, Platform: group.Platform})
				if e == nil && videoSchedule != nil && len(videoSchedule.Tiers) > 0 {
					unit := math.Inf(1)
					for _, t := range videoSchedule.Tiers {
						v := 0.0
						if t.Output != nil {
							v = *t.Output
						}
						unit = math.Min(unit, v)
					}
					prices["request"] = unit * tier.CreditUnits * rate * resolvedChannelTimeMultiplier(resolved, at)
				}
			} else if resolved != nil && resolved.Mode == BillingModePerRequest {
				// For context-dependent request rates the lowest selling tier is safe.
				v := resolved.DefaultPerRequestPrice
				for _, t := range resolved.RequestTiers {
					if t.PerRequestPrice != nil {
						v = math.Min(v, *t.PerRequestPrice)
					}
				}
				prices["request"] = v * rate
			}
			continue
		}
		if tier.Unit != "USD/1M tokens" {
			continue
		}
		if schedule == nil && scheduleErr == nil {
			schedule, scheduleErr = s.billing.ResolveContextPricingSchedule(ctx, s.resolver, ContextPricingScheduleInput{Model: model, Group: group, Platform: group.Platform})
		}
		if scheduleErr != nil || schedule == nil || len(schedule.Tiers) == 0 {
			continue
		}
		// Upstream adapters publish conservative maxima across context tiers. Compare
		// them with the lowest local unit price, so long/short prompts cannot lose money.
		for i, t := range schedule.Tiers {
			components := map[string]*float64{"input_price": t.Input, "output_price": t.Output, "cache_read_price": t.CacheRead, "cache_write_price": t.CacheWrite, "cache_write_1h_price": t.CacheWrite1h}
			for key, p := range components {
				v := 0.0
				if p != nil {
					v = *p * 1e6 * rate * resolvedChannelTimeMultiplier(resolved, at)
				}
				if i == 0 {
					prices[key] = v
				} else {
					prices[key] = math.Min(prices[key], v)
				}
			}
		}
		// Image token prices are not part of the context display schedule. Probe the
		// billing function for each context segment, including inherited overrides.
		for _, key := range []string{"image_input_price", "image_output_price"} {
			v := math.Inf(1)
			for _, t := range schedule.Tiers {
				n := t.MinTokens + 1
				base := UsageTokens{InputTokens: n}
				probe := base
				if key == "image_input_price" {
					probe.ImageInputTokens = n
				} else {
					probe.OutputTokens = 1
					probe.ImageOutputTokens = 1
				}
				a, e := s.billing.CalculateCostUnified(CostInput{Ctx: ctx, Model: model, Group: group, GroupID: &group.ID, Tokens: probe, RateMultiplier: rate, PricingAt: at, Resolver: s.resolver, Resolved: resolved})
				if e != nil {
					continue
				}
				unit := a.ImageInputCost / float64(n)
				if key == "image_output_price" {
					unit = a.ImageOutputCost
				}
				v = math.Min(v, unit*rate*1e6)
			}
			if !math.IsInf(v, 0) {
				prices[key] = v
			}
		}
	}
	return out, nil
}

func (s *UpstreamSiteService) PricePreview(ctx context.Context, id string, binding SiteBinding) (*SiteBinding, error) {
	site, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	s.refreshBindingPrice(ctx, site, &binding)
	return &binding, nil
}
func (s *UpstreamSiteService) refreshBindingPrice(ctx context.Context, site *UpstreamSite, b *SiteBinding) {
	policy := s.pricing.apply(ctx, BuildSiteAccountPolicy(site, b), false)
	b.Limits = policy.Limits
	b.PriceTiers = policy.Tiers
	if b.AccountID == 0 || b.Status == "error" {
		return
	}
	allowed := 0
	for _, tier := range policy.Tiers {
		if siteTierReason(policy, tier.Key, time.Now()) == "" {
			allowed++
		}
	}
	if b.Status == "preview" {
		return
	}
	b.Status = "blocked"
	if allowed > 0 {
		b.Status = "partial"
		if allowed == len(policy.Tiers) {
			b.Status = "ready"
		}
	}
}

// Only tier switches are writable. Keep switches for temporarily absent tiers.
func siteMergeTierSwitches(previous, input []SiteTierLimit) []SiteTierLimit {
	result := append([]SiteTierLimit(nil), previous...)
	for _, selection := range input {
		found := false
		for i := range result {
			if result[i].Key == selection.Key {
				result[i].Enabled = selection.Enabled
				found = true
				break
			}
		}
		if !found {
			result = append(result, SiteTierLimit{Key: selection.Key, Unit: selection.Unit, Enabled: selection.Enabled})
		}
	}
	return result
}

// Admission and customer billing must resolve the same local model even if the
// upstream response or a channel uses a different name.
func siteBillingFields(account *Account, fields ChannelUsageFields) ChannelUsageFields {
	if p, managed := account.SitePolicy(); managed && p.LocalModel != "" {
		fields.BillingModelSource = BillingModelSourceRequested
		if fields.OriginalModel == "" {
			fields.OriginalModel = p.LocalModel
		}
	}
	return fields
}
