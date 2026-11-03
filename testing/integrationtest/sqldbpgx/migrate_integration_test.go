//go:build integration

package sqldbpgx

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/redact"
	kitpgx "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
	"github.com/bds421/rho-kit/infra/v2/sqldb/migrate"
)

// TestMigrate_IndependentSetsShareDatabase verifies two migration sets with
// overlapping version numbers apply independently when each has its own
// goose version table, and that re-running either is a no-op.
func TestMigrate_IndependentSetsShareDatabase(t *testing.T) {
	dsn := startPostgres(t)
	pool, err := kitpgx.Connect(context.Background(), kitpgx.Config{DSN: dsn, AllowPlaintextLoopbackForTests: true})
	require.NoError(t, err)
	defer pool.Close()

	sqlDB := stdlib.OpenDBFromPool(pool.Pool())
	defer func() { _ = sqlDB.Close() }()

	ctx := context.Background()
	orders := migrate.Config{TableName: "orders_schema_version", Dir: fstest.MapFS{
		"00001_orders.sql": {Data: []byte("-- +goose Up\nCREATE TABLE orders (id INT);\n-- +goose Down\nDROP TABLE orders;\n")},
	}}
	billing := migrate.Config{TableName: "billing_schema_version", Dir: fstest.MapFS{
		"00001_invoices.sql": {Data: []byte("-- +goose Up\nCREATE TABLE invoices (id INT);\n-- +goose Down\nDROP TABLE invoices;\n")},
	}}

	applied, err := migrate.Up(ctx, sqlDB, orders)
	require.NoError(t, err)
	assert.Equal(t, 1, applied)
	applied, err = migrate.Up(ctx, sqlDB, billing)
	require.NoError(t, err)
	assert.Equal(t, 1, applied, "version 1 of a second set must not collide with the first")

	for _, cfg := range []migrate.Config{orders, billing} {
		applied, err = migrate.Up(ctx, sqlDB, cfg)
		require.NoError(t, err)
		assert.Zero(t, applied)
	}

	var exists bool
	require.NoError(t, sqlDB.QueryRowContext(ctx,
		"SELECT to_regclass('public.goose_db_version') IS NOT NULL").Scan(&exists))
	assert.False(t, exists, "default version table must not be created for named sets")

	require.NoError(t, migrate.Down(ctx, sqlDB, billing))
	require.NoError(t, sqlDB.QueryRowContext(ctx,
		"SELECT to_regclass('public.orders') IS NOT NULL").Scan(&exists))
	assert.True(t, exists, "rolling back one set must not touch another")
}

// TestMigrate_RefusesADatabaseAheadOfTheFiles verifies that a database
// migrated by a longer chain than the files on hand (a newer release, or a
// chain since squashed into one baseline) is refused instead of taken as up
// to date, and that a database at the newest file still migrates.
func TestMigrate_RefusesADatabaseAheadOfTheFiles(t *testing.T) {
	dsn := startPostgres(t)
	pool, err := kitpgx.Connect(context.Background(), kitpgx.Config{DSN: dsn, AllowPlaintextLoopbackForTests: true})
	require.NoError(t, err)
	defer pool.Close()

	sqlDB := stdlib.OpenDBFromPool(pool.Pool())
	defer func() { _ = sqlDB.Close() }()

	ctx := context.Background()
	chain := migrate.Config{TableName: "accounts_schema_version", Dir: fstest.MapFS{
		"00001_accounts.sql": {Data: []byte("-- +goose Up\nCREATE TABLE accounts (id INT);\n-- +goose Down\nDROP TABLE accounts;\n")},
		"00002_owner.sql":    {Data: []byte("-- +goose Up\nALTER TABLE accounts ADD owner TEXT;\n-- +goose Down\nALTER TABLE accounts DROP owner;\n")},
	}}
	baseline := migrate.Config{TableName: "accounts_schema_version", Dir: fstest.MapFS{
		"00001_baseline.sql": {Data: []byte("-- +goose Up\nCREATE TABLE IF NOT EXISTS accounts (id INT, owner TEXT);\n-- +goose Down\nDROP TABLE accounts;\n")},
	}}

	applied, err := migrate.Up(ctx, sqlDB, chain)
	require.NoError(t, err)
	assert.Equal(t, 2, applied)
	applied, err = migrate.Up(ctx, sqlDB, chain)
	require.NoError(t, err, "a database at the newest file must still migrate")
	assert.Zero(t, applied)

	applied, err = migrate.Up(ctx, sqlDB, baseline)
	require.ErrorIs(t, err, migrate.ErrAhead)
	assert.Zero(t, applied)
	assert.Contains(t, err.Error(), "accounts_schema_version")
	assert.Contains(t, err.Error(), "version 2")
	assert.Contains(t, err.Error(), "00001_baseline.sql")
	assert.Contains(t, redact.SafeMessages(err), "accounts_schema_version records version 2")
}
