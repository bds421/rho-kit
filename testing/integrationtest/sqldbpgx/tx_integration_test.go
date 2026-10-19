//go:build integration

package sqldbpgx

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kitpgx "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
)

// TestInTx_CommitRollbackPanicJoin drives Pool.InTx against PostgreSQL.
func TestInTx_CommitRollbackPanicJoin(t *testing.T) {
	dsn := startPostgres(t)
	pool, err := kitpgx.Connect(context.Background(), kitpgx.Config{DSN: dsn, AllowPlaintextLoopbackForTests: true})
	require.NoError(t, err)
	defer pool.Close()
	ctx := context.Background()
	_, err = pool.Pool().Exec(ctx, "CREATE TABLE tx_items (name TEXT PRIMARY KEY)")
	require.NoError(t, err)
	insert := func(ctx context.Context, name string) error {
		_, err := pool.Querier(ctx).Exec(ctx, "INSERT INTO tx_items (name) VALUES ($1)", name)
		return err
	}
	exists := func(name string) bool {
		var n int
		require.NoError(t, pool.Pool().QueryRow(ctx, "SELECT count(*) FROM tx_items WHERE name = $1", name).Scan(&n))
		return n == 1
	}

	require.NoError(t, pool.InTx(ctx, func(ctx context.Context) error { return insert(ctx, "committed") }))
	assert.True(t, exists("committed"))

	boom := errors.New("boom")
	err = pool.InTx(ctx, func(ctx context.Context) error {
		require.NoError(t, insert(ctx, "rolled-back"))
		return boom
	})
	require.ErrorIs(t, err, boom)
	assert.False(t, exists("rolled-back"))

	assert.Panics(t, func() {
		_ = pool.InTx(ctx, func(ctx context.Context) error {
			require.NoError(t, insert(ctx, "panicked"))
			panic("kaboom")
		})
	})
	assert.False(t, exists("panicked"))

	// A nested InTx joins the outer transaction: the outer rollback undoes it.
	err = pool.InTx(ctx, func(ctx context.Context) error {
		require.NoError(t, pool.InTx(ctx, func(ctx context.Context) error { return insert(ctx, "joined") }))
		assert.False(t, exists("joined"), "joined write must not be committed on its own")
		return boom
	})
	require.ErrorIs(t, err, boom)
	assert.False(t, exists("joined"))
}
