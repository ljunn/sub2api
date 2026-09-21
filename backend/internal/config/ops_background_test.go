package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadOpsBackgroundTasksDeploymentOverride(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.True(t, cfg.Ops.Enabled)
	require.True(t, cfg.Ops.BackgroundTasksEnabled())

	t.Setenv("OPS_DISABLE_BACKGROUND_TASKS", "true")
	cfg, err = Load()
	require.NoError(t, err)
	require.True(t, cfg.Ops.Enabled, "error collection and APIs must remain available")
	require.False(t, cfg.Ops.BackgroundTasksEnabled())

	t.Setenv("OPS_ENABLED", "false")
	t.Setenv("OPS_DISABLE_BACKGROUND_TASKS", "false")
	cfg, err = Load()
	require.NoError(t, err)
	require.False(t, cfg.Ops.BackgroundTasksEnabled(), "the Ops hard switch still wins")
}
