package service

import "github.com/Wei-Shaw/sub2api/internal/config"

// stickySessionsEnabled is the instance-wide override for automatic session
// affinity. Keep resource ownership (response IDs and media task IDs) intact.
func stickySessionsEnabled(cfg *config.Config) bool {
	return cfg == nil || !cfg.Gateway.Scheduling.DisableStickySessions
}

func (s *GatewayService) StickySessionsEnabled() bool {
	return s != nil && stickySessionsEnabled(s.cfg)
}

func (s *OpenAIGatewayService) StickySessionsEnabled() bool {
	return s != nil && stickySessionsEnabled(s.cfg)
}
