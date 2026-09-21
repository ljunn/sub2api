package service

import (
	"context"
	"sort"
	"strings"
)

// GeminiConfiguredModelIDs reports client-facing names across the whole group.
// A group of explicitly restricted accounts must never inherit one upstream's
// catalogue: it can contain unsupported names and omit another binding's model.
func (s *GeminiMessagesCompatService) GeminiConfiguredModelIDs(ctx context.Context, groupID *int64) (models []string, complete bool, err error) {
	accounts, err := s.listSchedulableAccountsOnce(ctx, groupID, PlatformGemini, true)
	if err != nil {
		return nil, false, err
	}
	seen := map[string]bool{}
	complete = len(accounts) > 0
	for i := range accounts {
		a := &accounts[i]
		if a.Platform != PlatformGemini {
			continue
		}
		if policy, managed := a.SitePolicy(); managed {
			if policy.LocalModel != "" {
				seen[policy.LocalModel] = true
			}
			continue
		}
		mapping := a.GetModelMapping()
		if len(mapping) == 0 {
			complete = false
		}
		for name := range mapping {
			if strings.ContainsAny(name, "*?") {
				complete = false
				continue
			}
			seen[name] = true
		}
	}
	for name := range seen {
		models = append(models, name)
	}
	sort.Strings(models)
	return models, complete, nil
}
