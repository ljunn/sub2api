//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGroupEqualPriceDefaultsOffAndPersistsThroughRepository(t *testing.T) {
	ctx := context.Background()
	var id int64
	var allow bool
	require.NoError(t, integrationDB.QueryRowContext(ctx, "INSERT INTO groups (name, platform) VALUES ($1, 'openai') RETURNING id, allow_equal_price_scheduling", fmt.Sprintf("equal-price-%d", time.Now().UnixNano())).Scan(&id, &allow))
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, "DELETE FROM groups WHERE id = $1", id)
		require.NoError(t, err)
	})
	require.False(t, allow, "database default must reject equal prices")
	repo := NewGroupRepository(integrationEntClient, integrationDB)
	group, err := repo.GetByIDLite(ctx, id)
	require.NoError(t, err)
	require.False(t, group.AllowEqualPriceScheduling)
	for _, enabled := range []bool{true, false} {
		group.AllowEqualPriceScheduling = enabled
		require.NoError(t, repo.Update(ctx, group))
		loaded, err := repo.GetByIDLite(ctx, id)
		require.NoError(t, err)
		require.Equal(t, enabled, loaded.AllowEqualPriceScheduling)
	}
}
