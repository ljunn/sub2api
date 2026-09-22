package service

import (
	"context"
	"strings"
	"time"
)

// Scheduling is a live admin projection. Never overwrite the manual switch:
// doing so would prevent automatic recovery after a local price edit.
type SiteAccountScheduling struct {
	SiteID    string               `json:"site_id"`
	BindingID string               `json:"binding_id"`
	Status    string               `json:"status"`
	Reason    string               `json:"reason,omitempty"`
	CheckedAt time.Time            `json:"checked_at"`
	Tiers     []SiteSchedulingTier `json:"tiers"`
}

type SiteSchedulingTier struct {
	TrafficSupport *SiteTrafficStatus `json:"traffic_support,omitempty"`
	SitePriceTier
	Selling       map[string]float64 `json:"selling"`
	Ceiling       map[string]float64 `json:"ceiling"`
	Status        string             `json:"status"`
	PriorityScore *SitePriorityScore `json:"priority_score,omitempty"`
}

func (s *UpstreamSitePricing) AccountScheduling(ctx context.Context, account *Account) *SiteAccountScheduling {
	p, managed := account.SitePolicy()
	if !managed {
		return nil
	}
	p = s.apply(ctx, p, false)
	now := time.Now()
	out := &SiteAccountScheduling{SiteID: p.SiteID, BindingID: p.BindingID, Status: "blocked", CheckedAt: now, Tiers: []SiteSchedulingTier{}}
	pending, _ := account.Extra["upstream_site_preview_pending"].(bool)
	accountReason := ""
	if pending {
		accountReason = "preview"
	} else if !account.IsActive() || !account.Schedulable {
		accountReason = "disabled"
	} else if !account.IsSchedulable() {
		accountReason = "unavailable"
	}
	allowed := 0
	for _, tier := range p.Tiers {
		status := strings.TrimPrefix(siteTierReason(p, tier.Key, now), "site_")
		switch status {
		case "":
			status = "ready"
		case "price_equal":
			status = "equal"
		case "price_exceeded":
			status = "exceeded"
		case "price_expired":
			status = "expired"
		case "price_unknown":
			status = "unknown"
		case "tier_disabled":
			status = "disabled"
		}
		if status == "ready" && accountReason != "" {
			status = accountReason
		}
		row := SiteSchedulingTier{SitePriceTier: tier, Status: status}
		if s != nil {
			row.TrafficSupport = s.trafficStatus(ctx, account, p, tier.Key)
		}
		if row.TrafficSupport != nil && status != "ready" {
			row.TrafficSupport.State = "ineligible"
			row.TrafficSupport.Effective = 0
		}
		if status == "ready" {
			score := s.priorityScore(account.ID, p, tier.Key, now)
			if priority, ok := s.adminPriority(ctx, account, p, tier.Key, now); ok {
				score.Priority = priority
			}
			row.PriorityScore = &score
		}
		for _, limit := range p.Limits {
			if limit.Key == tier.Key {
				row.Selling, row.Ceiling = limit.Selling, limit.Limits
				break
			}
		}
		out.Tiers = append(out.Tiers, row)
		if status == "ready" {
			allowed++
		} else if out.Reason == "" {
			out.Reason = status
		}
	}
	if allowed > 0 {
		out.Status = "partial"
		if allowed == len(out.Tiers) {
			out.Status, out.Reason = "ready", ""
		}
	} else if out.Reason == "" {
		out.Reason = "unknown"
	}
	return out
}
