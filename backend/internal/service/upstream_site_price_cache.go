package service

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"
)

func sitePriceTierKnown(tier SitePriceTier) bool {
	if tier.Key == "" || tier.Unit == "" || tier.Reason != "" || len(tier.Prices) == 0 {
		return false
	}
	for _, price := range tier.Prices {
		if price < 0 || math.IsNaN(price) || math.IsInf(price, 0) {
			return false
		}
	}
	return true
}

func siteModelHasKnownPrice(model SiteModel) bool {
	if model.Reason != "" {
		return false
	}
	for _, tier := range model.Tiers {
		if sitePriceTierKnown(tier) {
			return true
		}
	}
	return false
}

// Keep the last usable snapshot for the same site/group/model when a refresh
// cannot price it. A successful catalogue removal remains authoritative.
// Cache failures are separate from Reason: they inform the administrator but
// do not turn a previously known purchase price into a scheduling veto.
func mergeSitePriceCache(site *UpstreamSite, models []SiteModel, failedGroups map[string]string, now time.Time) ([]SiteModel, []string) {
	old := make(map[string]SiteModel, len(site.Models))
	for _, model := range site.Models {
		if model.PriceUpdatedAt == nil && siteModelHasKnownPrice(model) {
			model.PriceUpdatedAt = site.LastSuccess
		}
		old[model.GroupID+"\x00"+model.Model] = model
	}
	seen := make(map[string]bool, len(models))
	warnings := []string{}
	for i := range models {
		model := &models[i]
		key := model.GroupID + "\x00" + model.Model
		seen[key] = true
		previous, exists := old[key]
		canReuse := exists && previous.PriceUpdatedAt != nil && siteModelHasKnownPrice(previous)
		if model.Reason != "" || len(model.Tiers) == 0 {
			if canReuse {
				reason := model.Reason
				if reason == "" {
					reason = "未返回采购价"
				}
				*model = previous
				model.PriceSyncError = reason
			}
		} else {
			model.PriceUpdatedAt = &now
			// Only reuse individual tiers if protocol/capability metadata has
			// not changed. Never mix an old SKU price with a new SKU definition.
			compatible := canReuse && model.Platform == previous.Platform && model.Image == previous.Image &&
				model.VideoAPIFormat == previous.VideoAPIFormat && reflect.DeepEqual(model.Wuzu, previous.Wuzu) &&
				reflect.DeepEqual(model.VividAI, previous.VividAI) && reflect.DeepEqual(model.LongXia, previous.LongXia)
			for j, tier := range model.Tiers {
				if sitePriceTierKnown(tier) || !compatible {
					continue
				}
				for _, prior := range previous.Tiers {
					if prior.Key == tier.Key && prior.Unit == tier.Unit && sitePriceTierKnown(prior) {
						model.Tiers[j] = prior
						model.PriceUpdatedAt = previous.PriceUpdatedAt
						model.PriceSyncError = "部分档位未取得有效采购价"
						break
					}
				}
			}
			if !siteModelHasKnownPrice(*model) {
				model.PriceUpdatedAt = nil
			}
		}
		if model.PriceSyncError != "" {
			warnings = append(warnings, sitePriceCacheWarning(*model))
		}
	}
	// A failed group lookup is not an authoritative empty model list. Preserve
	// that group's previous models without retaining groups explicitly removed.
	for _, prior := range site.Models {
		if reason := failedGroups[prior.GroupID]; reason != "" && !seen[prior.GroupID+"\x00"+prior.Model] {
			model := old[prior.GroupID+"\x00"+prior.Model]
			if model.PriceUpdatedAt != nil && siteModelHasKnownPrice(model) {
				model.PriceSyncError = reason
				warnings = append(warnings, sitePriceCacheWarning(model))
			}
			models = append(models, model)
		}
	}
	return models, warnings
}

func sitePriceCacheWarning(model SiteModel) string {
	return fmt.Sprintf("%s（%s）采购价刷新未完成，继续使用上次有效价格：%s", model.Model, model.GroupName,
		strings.TrimSuffix(model.PriceSyncError, "，已阻止调度"))
}

func (a *siteAdapter) markGroupCatalogFailed(groupID string, err error) {
	if a.failedGroups == nil {
		a.failedGroups = map[string]string{}
	}
	a.failedGroups[groupID] = err.Error()
}
