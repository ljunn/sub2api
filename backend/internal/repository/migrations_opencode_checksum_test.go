package repository

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeForkMigrationChecksumCompatibility(t *testing.T) {
	const name = "238_opencode_go_platform.sql"
	const forkChecksum = "d310f134e119bd0b01c36e048841d04e1adc04a117c5c516ccdbbc8800742414"
	const upstreamChecksum = "6f987e251519bd3759e60da44620a5d777494cceb333b6ce394aa0ea536ef5a2"
	content, err := migrations.FS.ReadFile(name)
	require.NoError(t, err)
	require.Equal(t, upstreamChecksum, fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(string(content))))))
	require.True(t, isMigrationChecksumCompatible(name, forkChecksum, upstreamChecksum))
	require.True(t, isMigrationChecksumCompatible(name, upstreamChecksum, forkChecksum))
	require.False(t, isMigrationChecksumCompatible(name, "unknown", upstreamChecksum))
	require.False(t, isMigrationChecksumCompatible(name, forkChecksum, "unknown"))
	require.False(t, isMigrationChecksumCompatible("238_purge_unlimited_user_platform_quotas.sql", forkChecksum, upstreamChecksum))
}
