package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/google/uuid"
)

type SiteModelDiscovery struct {
	ID string     `json:"id,omitempty"`
	At *time.Time `json:"at,omitempty"`
}

// Separate from the price cache so changing credentials or conversion rates
// does not erase unread discoveries or turn existing models into new models.
type SiteModelCatalogue struct {
	Entries map[string]SiteModelDiscovery `json:"entries"`
}

func siteModelKey(model SiteModel) string {
	key, _ := json.Marshal([2]string{model.GroupID, model.Model})
	return string(key)
}

func seedSiteModelCatalogue(site *UpstreamSite) {
	if site.ModelCatalogue != nil || (site.LastSuccess == nil && len(site.Models) == 0) {
		return
	}
	site.ModelCatalogue = &SiteModelCatalogue{Entries: map[string]SiteModelDiscovery{}}
	for _, model := range site.Models {
		site.ModelCatalogue.Entries[siteModelKey(model)] = SiteModelDiscovery{ID: model.DiscoveryID, At: model.DiscoveredAt}
	}
}

func trackSiteModelDiscoveries(site *UpstreamSite, models []SiteModel, now time.Time) {
	seedSiteModelCatalogue(site)
	entries := make(map[string]SiteModelDiscovery, len(models))
	for i := range models {
		key := siteModelKey(models[i])
		discovery := SiteModelDiscovery{}
		if site.ModelCatalogue != nil {
			var found bool
			discovery, found = site.ModelCatalogue.Entries[key]
			if !found {
				discovery = SiteModelDiscovery{ID: uuid.NewString(), At: &now}
			}
		}
		entries[key] = discovery
		models[i].DiscoveryID, models[i].DiscoveredAt = discovery.ID, discovery.At
		models[i].Unread = false
	}
	site.ModelCatalogue = &SiteModelCatalogue{Entries: entries}
}

func siteModelsReadKey(userID int64, siteID string) string {
	return fmt.Sprintf("upstream_site_models_read:%d:%s", userID, siteID)
}

func parseSiteModelsRead(raw string) (map[string]bool, error) {
	ids := []string{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &ids); err != nil {
			return nil, errors.New("读取模型已读记录失败")
		}
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	return seen, nil
}

// HTTP projection only: never store one administrator's unread state on a site.
func (s *UpstreamSiteService) AnnotateUnreadModels(ctx context.Context, sites []UpstreamSite, userID int64) error {
	if userID <= 0 || s.balanceSettings == nil {
		return errors.New("模型已读记录不可用")
	}
	keys := make([]string, len(sites))
	for i := range sites {
		keys[i] = siteModelsReadKey(userID, sites[i].ID)
	}
	if len(keys) == 0 {
		return nil
	}
	values, err := s.balanceSettings.GetMultiple(ctx, keys)
	if err != nil {
		return errors.New("读取模型已读记录失败")
	}
	for i := range sites {
		seen, err := parseSiteModelsRead(values[keys[i]])
		if err != nil {
			return err
		}
		for j := range sites[i].Models {
			m := &sites[i].Models[j]
			m.Unread = m.DiscoveryID != "" && !seen[m.DiscoveryID]
		}
		// Internal baseline metadata is not part of the admin API.
		sites[i].ModelCatalogue = nil
	}
	return nil
}

func (s *UpstreamSiteService) MarkModelsRead(ctx context.Context, siteID string, userID int64, ids []string) ([]string, error) {
	if userID <= 0 || s.balanceSettings == nil {
		return nil, errors.New("模型已读记录不可用")
	}
	if len(ids) == 0 || len(ids) > 100 {
		return nil, errors.New("每次可标记 1 至 100 个模型")
	}
	unlock, err := s.repo.Lock(ctx, siteID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	site, err := s.repo.Get(ctx, siteID)
	if err != nil {
		return nil, err
	}
	key := siteModelsReadKey(userID, siteID)
	values, err := s.balanceSettings.GetMultiple(ctx, []string{key})
	if err != nil {
		return nil, errors.New("读取模型已读记录失败")
	}
	seen, err := parseSiteModelsRead(values[key])
	if err != nil {
		return nil, err
	}
	requested := map[string]bool{}
	for _, id := range ids {
		requested[id] = true
	}
	acknowledged, retained := []string{}, []string{}
	for _, discovery := range site.ModelCatalogueEntries() {
		if discovery.ID == "" {
			continue
		}
		if requested[discovery.ID] {
			acknowledged = append(acknowledged, discovery.ID)
			seen[discovery.ID] = true
		}
		if seen[discovery.ID] {
			retained = append(retained, discovery.ID)
		}
	}
	sort.Strings(retained)
	sort.Strings(acknowledged)
	raw, _ := json.Marshal(retained)
	if err := s.balanceSettings.Set(ctx, key, string(raw)); err != nil {
		return nil, errors.New("保存模型已读记录失败")
	}
	return acknowledged, nil
}

func (site *UpstreamSite) ModelCatalogueEntries() map[string]SiteModelDiscovery {
	if site.ModelCatalogue != nil {
		return site.ModelCatalogue.Entries
	}
	return nil
}
