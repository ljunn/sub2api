package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadDisableStickySessionsOverride(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.False(t, cfg.Gateway.Scheduling.DisableStickySessions)
	t.Setenv("GATEWAY_SCHEDULING_DISABLE_STICKY_SESSIONS", "true")
	cfg, err = Load()
	require.NoError(t, err)
	require.True(t, cfg.Gateway.Scheduling.DisableStickySessions)
}
