package service

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"time"
)

const siteSupportSettingKey = "upstream_site_traffic_support_v1"

// Stored outside the site document: an older instance's catalogue refresh must
// not erase new controls while preview and production share PostgreSQL.
type SiteTrafficSupport struct {
	Enabled   bool       `json:"enabled"`
	Percent   float64    `json:"percent"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}
type siteSupportEntry struct {
	SiteID   string             `json:"site_id"`
	GroupID  int64              `json:"group_id"`
	Platform string             `json:"platform"`
	Model    string             `json:"model"`
	Config   SiteTrafficSupport `json:"config"`
}
type siteSupportSettings struct {
	repo    SettingRepository
	mu      sync.Mutex
	until   time.Time
	entries map[string]siteSupportEntry
}

func (s *siteSupportSettings) read(ctx context.Context) (map[string]siteSupportEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Now().Before(s.until) {
		return s.entries, nil
	}
	entries := map[string]siteSupportEntry{}
	if s.repo != nil {
		raw, err := s.repo.GetValue(ctx, siteSupportSettingKey)
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			return nil, err
		}
		if raw != "" {
			if err = json.Unmarshal([]byte(raw), &entries); err != nil {
				return nil, err
			}
		}
	}
	s.entries, s.until = entries, time.Now().Add(3*time.Second)
	return entries, nil
}
func (s *siteSupportSettings) invalidate() { s.mu.Lock(); s.until = time.Time{}; s.mu.Unlock() }
func (c SiteTrafficSupport) active(now time.Time) bool {
	return c.Enabled && c.Percent > 0 && (c.ExpiresAt == nil || now.Before(*c.ExpiresAt))
}

func (s *UpstreamSiteService) SaveTrafficSupport(ctx context.Context, siteID, bindingID string, config SiteTrafficSupport) error {
	if s.pricing == nil || s.pricing.support.repo == nil {
		return errors.New("流量扶持设置不可用")
	}
	if math.IsNaN(config.Percent) || math.IsInf(config.Percent, 0) || config.Percent < 0 || config.Percent > 95 || (config.Enabled && config.Percent < 1) {
		return errors.New("扶持比例须为 1%～95%，为恢复试调保留 5%")
	}
	if config.Enabled && config.ExpiresAt != nil && !config.ExpiresAt.After(time.Now()) {
		return errors.New("扶持截止时间必须晚于当前时间")
	}
	// Serialize allocation across different sites sharing the same model pool.
	unlock, err := s.repo.Lock(ctx, "traffic-support-v1")
	if err != nil {
		return err
	}
	defer unlock()
	site, err := s.repo.Get(ctx, siteID)
	if err != nil {
		return err
	}
	var binding *SiteBinding
	for i := range site.Bindings {
		if site.Bindings[i].ID == bindingID {
			binding = &site.Bindings[i]
			break
		}
	}
	if binding == nil || binding.AccountID == 0 {
		return errors.New("请先保存有效的模型绑定")
	}
	s.pricing.support.invalidate()
	entries, err := s.pricing.support.read(ctx)
	if err != nil {
		return err
	}
	copy := make(map[string]siteSupportEntry, len(entries)+1)
	for id, e := range entries {
		copy[id] = e
	}
	copy[bindingID] = siteSupportEntry{siteID, binding.LocalGroupID, binding.Platform, binding.LocalModel, config}
	// Only existing bindings consume the configurable budget (deleted entries do not).
	sites, err := s.repo.List(ctx)
	if err != nil {
		return err
	}
	total := 0.0
	for _, site := range sites {
		for _, b := range site.Bindings {
			e, ok := copy[b.ID]
			if ok && e.Config.active(time.Now()) && b.LocalGroupID == binding.LocalGroupID && b.Platform == binding.Platform && b.LocalModel == binding.LocalModel {
				total += e.Config.Percent
			}
		}
	}
	if total > 95+1e-9 {
		return errors.New("同一分组、模型的扶持目标合计不能超过 95%")
	}
	raw, err := json.Marshal(copy)
	if err != nil {
		return err
	}
	if err = s.pricing.support.repo.Set(ctx, siteSupportSettingKey, string(raw)); err != nil {
		return err
	}
	s.pricing.support.invalidate()
	return nil
}
func (s *UpstreamSiteService) AnnotateTrafficSupport(ctx context.Context, site *UpstreamSite) error {
	if s.pricing == nil {
		return nil
	}
	entries, err := s.pricing.support.read(ctx)
	if err != nil {
		return err
	}
	for i := range site.Bindings {
		b := &site.Bindings[i]
		config := SiteTrafficSupport{Percent: 20}
		if e, ok := entries[b.ID]; ok {
			config = e.Config
		}
		b.TrafficSupport = &config
	}
	return nil
}

// Health gates only extra allocation, never the account's normal eligibility.
// Replay bounded history so a restart cannot reset a degraded account to healthy.
func siteSupportHealth(values []sitePerformanceSample, target float64) (float64, string) {
	stage := math.Min(10, target)
	degraded, streak, failures := false, 0, 0
	var phase []bool
	sinceUpgrade := 0
	for _, v := range values {
		if v.Success {
			streak++
			failures = 0
		} else {
			failures++
			streak = 0
		}
		if degraded {
			if streak >= 3 {
				degraded = false
				stage = math.Min(10, target)
				phase = nil
				sinceUpgrade = 0
			}
			continue
		}
		phase = append(phase, v.Success)
		sinceUpgrade++
		if len(phase) > 20 {
			phase = phase[len(phase)-20:]
		}
		successes := 0
		for _, ok := range phase {
			if ok {
				successes++
			}
		}
		if failures >= 3 || (len(phase) == 20 && successes < 18) {
			degraded = true
			phase = nil
			continue
		}
		if len(phase) == 20 && successes >= 19 && sinceUpgrade >= 20 {
			stage = math.Min(target, stage+10)
			sinceUpgrade = 0
		}
	}
	if degraded {
		return 0, "degraded"
	}
	if stage < target {
		return stage, "ramping"
	}
	return stage, "active"
}
