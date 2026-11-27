//go:build integration

package auditlogpg

import (
	"context"
	"crypto/ed25519"
	"io/fs"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	postgresstore "github.com/bds421/rho-kit/observability/auditlog/postgres/v2"
	"github.com/bds421/rho-kit/observability/v2/auditlog"
)

const kitSchema = "kit audit"

func migrateInSchema(t *testing.T, pool *pgxpool.Pool, schema string) error {
	t.Helper()
	dir, err := postgresstore.MigrationsInSchema(schema)
	require.NoError(t, err)
	sub, err := fs.Sub(dir, "migrations")
	require.NoError(t, err)
	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub, goose.WithTableName("public.kit_audit_migrations"))
	require.NoError(t, err)
	_, err = provider.Up(t.Context())
	return err
}

// The tables, their indexes and pg_trgm land in the schema, the session's
// search_path is left as it was, and a Store in the schema works there
// with nothing on the search_path.
func TestPostgres_TablesInASchema(t *testing.T) {
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(startPostgres(t))
	require.NoError(t, err)
	config.MaxConns = 1 // the migrations' connection is the one asked afterwards
	pool, err := pgxpool.NewWithConfig(ctx, config)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, `CREATE SCHEMA "kit audit"`)
	require.NoError(t, err)
	var before string
	require.NoError(t, pool.QueryRow(ctx, `SHOW search_path`).Scan(&before))

	require.NoError(t, migrateInSchema(t, pool, kitSchema))

	var outside int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM pg_tables WHERE tablename LIKE 'audit_log_%' AND schemaname <> $1`, kitSchema).Scan(&outside))
	require.Zero(t, outside, "no table may land outside the schema")
	var indexes []string
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT array_agg(indexname ORDER BY indexname) FROM pg_indexes WHERE schemaname = $1 AND tablename = 'audit_log_events'`, kitSchema).Scan(&indexes))
	assert.Contains(t, indexes, "idx_audit_log_events_search")
	assert.Contains(t, indexes, "idx_audit_log_events_prev_hmac")
	var after string
	require.NoError(t, pool.QueryRow(ctx, `SHOW search_path`).Scan(&after))
	assert.Equal(t, before, after, "the migrations leave the session's search_path as it was")

	store := postgresstore.New(pool, postgresstore.WithSchema(kitSchema))
	logger := auditlog.New(store, auditlog.WithChainKey(testChainKey), auditlog.WithCursorKey(testCursorKey))
	defer func() { _ = logger.Close() }()
	now := time.Now().UTC().Truncate(time.Microsecond)
	appendAged(t, logger, now, 500, 450, 10)
	got, _, err := logger.List(ctx, auditlog.Filter{Search: "aged"}, "", 10)
	require.NoError(t, err)
	assert.Len(t, got, 3)
	tail, _, err := store.Tail(ctx, "", 0, 10)
	require.NoError(t, err)
	assert.Len(t, tail, 3)
	kept, err := logger.Verify(ctx, auditlog.Verification{})
	require.NoError(t, err)

	c, err := auditlog.ComputeCut(ctx, store, []auditlog.ChainKey{{Key: testChainKey}}, auditlog.Verification{}, now.Add(-testRetention), now)
	require.NoError(t, err)
	public, private, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	signer, err := auditlog.NewEd25519CutSigner(private)
	require.NoError(t, err)
	verifier, err := auditlog.NewEd25519CutVerifier(public)
	require.NoError(t, err)
	text, err := c.Encode(signer)
	require.NoError(t, err)
	decoded, err := auditlog.DecodeCut(text, verifier, now, time.Hour)
	require.NoError(t, err)
	removed, err := store.ApplyCut(ctx, decoded, testRetentionKey, testMinimum, now)
	require.NoError(t, err)
	assert.EqualValues(t, 2, removed)
	record, err := store.RetentionRecord(ctx, testRetentionKey)
	require.NoError(t, err)
	v := record.Verification(testRetention, testMinimum, now)
	v.Anchor = kept
	_, err = logger.Verify(ctx, v)
	require.NoError(t, err)
}

// PostgreSQL skips a missing schema on the search_path, so without the
// check the tables would land in public.
func TestPostgres_MigrationsInAMissingSchemaFail(t *testing.T) {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, startPostgres(t))
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	require.Error(t, migrateInSchema(t, pool, "absent"))
	var tables int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE tablename LIKE 'audit_log_%'`).Scan(&tables))
	require.Zero(t, tables)
}
