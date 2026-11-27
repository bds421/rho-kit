//go:build integration

package outboxpg

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"

	outboxpg "github.com/bds421/rho-kit/infra/outbox/postgres/v2"
	"github.com/bds421/rho-kit/infra/v2/sqldb/migrate"
)

// TestStore_WithSchema migrates the tables into a schema that is not on
// search_path and runs the store and the inbox against it, so every
// statement must name the schema.
func TestStore_WithSchema(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, startPostgres(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `CREATE SCHEMA "kit outbox"`)
	require.NoError(t, err)

	dir, err := outboxpg.MigrationsInSchema("kit outbox")
	require.NoError(t, err)
	sub, err := fs.Sub(dir, "migrations")
	require.NoError(t, err)
	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	_, err = migrate.Up(ctx, sqlDB, migrate.Config{Dir: sub, TableName: "public.kit_outbox_migrations"})
	require.NoError(t, err)

	var public int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_tables WHERE tablename IN ('outbox_entries', 'inbox_entries') AND schemaname <> 'kit outbox'`).Scan(&public))
	require.Zero(t, public, "no table may land outside the schema")
	var indexes []string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT array_agg(indexname ORDER BY indexname) FROM pg_indexes WHERE schemaname = 'kit outbox' AND tablename = 'outbox_entries'`).Scan(&indexes))
	require.Equal(t, []string{
		"idx_outbox_entries_failed_updated", "idx_outbox_entries_pending_due",
		"idx_outbox_entries_processing_claim_token", "idx_outbox_entries_processing_updated",
		"idx_outbox_entries_published_at", "outbox_entries_pkey",
	}, indexes, "the renames must find the indexes in the schema")

	store := outboxpg.NewWithOptions(pool, outboxpg.WithSchema("kit outbox"))
	require.NoError(t, store.Insert(ctx, mustEntry()))
	n, err := store.CountPending(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	claimed, err := store.FetchPending(ctx, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.NoError(t, store.MarkPublished(ctx, claimed[0].ID.String(), time.Now()))
	deleted, err := store.DeletePublishedBefore(ctx, time.Now().Add(time.Minute))
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)

	inbox := outboxpg.NewInbox(pool, outboxpg.WithInboxSchema("kit outbox"))
	res, err := inbox.Process(ctx, "consumer", "msg-1", func(context.Context) error { return nil })
	require.NoError(t, err)
	require.False(t, res.Duplicate)
	count, err := inbox.Count(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, count)
}

// TestMigrationsInSchema_MissingSchemaFails: PostgreSQL skips a missing
// schema on the search_path, so without the check the tables would land in
// public.
func TestMigrationsInSchema_MissingSchemaFails(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, startPostgres(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	dir, err := outboxpg.MigrationsInSchema("absent")
	require.NoError(t, err)
	sub, err := fs.Sub(dir, "migrations")
	require.NoError(t, err)
	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	_, err = migrate.Up(ctx, sqlDB, migrate.Config{Dir: sub})
	require.Error(t, err)

	var tables int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_tables WHERE tablename IN ('outbox_entries', 'inbox_entries')`).Scan(&tables))
	require.Zero(t, tables)
}
