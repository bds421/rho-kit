//go:build integration

package apikeypg

import (
	"context"
	"io/fs"
	"net"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	postgresstore "github.com/bds421/rho-kit/data/apikey/postgres/v2"
	"github.com/bds421/rho-kit/infra/sqldb/dbtest/v2"
	"github.com/bds421/rho-kit/security/v2/apikey"
)

func startPostgres(t *testing.T) string {
	t.Helper()
	cfg := dbtest.StartPostgres(t, "apikey_test")
	q := url.Values{}
	for k, v := range cfg.Options {
		q.Set(k, v)
	}
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(cfg.User, cfg.Password),
		Host:     net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
		Path:     cfg.Name,
		RawQuery: q.Encode(),
	}
	return u.String()
}

func openAndMigrate(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })

	sub, err := fs.Sub(postgresstore.Migrations, "migrations")
	require.NoError(t, err)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	require.NoError(t, err)
	_, err = provider.Up(ctx)
	require.NoError(t, err)
	return pool
}

func TestPostgres_Live_IssueLookupVerify(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	store := postgresstore.New(openAndMigrate(t, startPostgres(t)))

	key, token, err := apikey.Generate(apikey.GenerateOptions{
		Kind:      apikey.KindAPI,
		Scopes:    []string{"orders.read", "orders.write"},
		Owner:     "tenant-1",
		ExpiresAt: now.Add(24 * time.Hour),
		Now:       now,
	})
	require.NoError(t, err)
	require.NoError(t, store.Insert(ctx, key))

	id, secret, err := apikey.Parse(token.RevealString(), apikey.DefaultPrefix)
	require.NoError(t, err)

	got, err := store.FindByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, key.Hash, got.Hash)
	assert.Equal(t, []string{"orders.read", "orders.write"}, got.Scopes)
	assert.Equal(t, apikey.KindAPI, got.Kind)
	assert.Equal(t, now.Add(24*time.Hour), got.ExpiresAt)
	assert.True(t, got.RevokedAt.IsZero(), "fresh key is not revoked")
	assert.NoError(t, got.Verify(secret, now))
}

func TestPostgres_Live_DuplicateInsertConflicts(t *testing.T) {
	ctx := context.Background()
	store := postgresstore.New(openAndMigrate(t, startPostgres(t)))
	key, _, err := apikey.Generate(apikey.GenerateOptions{Owner: "o"})
	require.NoError(t, err)
	require.NoError(t, store.Insert(ctx, key))
	assert.Error(t, store.Insert(ctx, key))
}

func TestPostgres_Live_RevokeIsIdempotentAndReflected(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	store := postgresstore.New(openAndMigrate(t, startPostgres(t)))
	key, token, err := apikey.Generate(apikey.GenerateOptions{Owner: "o", Now: now})
	require.NoError(t, err)
	require.NoError(t, store.Insert(ctx, key))

	require.NoError(t, store.Revoke(ctx, key.ID, now))
	require.NoError(t, store.Revoke(ctx, key.ID, now.Add(time.Hour)), "revoke is idempotent")

	got, err := store.FindByID(ctx, key.ID)
	require.NoError(t, err)
	assert.Equal(t, now, got.RevokedAt, "first revocation time is retained")

	_, secret, _ := apikey.Parse(token.RevealString(), apikey.DefaultPrefix)
	assert.ErrorIs(t, got.Verify(secret, now), apikey.ErrRevoked)
}

func TestPostgres_Live_RevokeMissingIsNotFound(t *testing.T) {
	ctx := context.Background()
	store := postgresstore.New(openAndMigrate(t, startPostgres(t)))
	assert.Error(t, store.Revoke(ctx, "00000000-0000-0000-0000-000000000000", time.Now()))
}

func TestPostgres_Live_FindMissingIsNotFound(t *testing.T) {
	ctx := context.Background()
	store := postgresstore.New(openAndMigrate(t, startPostgres(t)))
	_, err := store.FindByID(ctx, "missing")
	assert.Error(t, err)
}

func TestPostgres_Live_ListByOwner(t *testing.T) {
	ctx := context.Background()
	store := postgresstore.New(openAndMigrate(t, startPostgres(t)))
	for i := 0; i < 3; i++ {
		key, _, err := apikey.Generate(apikey.GenerateOptions{Owner: "owner-a"})
		require.NoError(t, err)
		require.NoError(t, store.Insert(ctx, key))
	}
	other, _, err := apikey.Generate(apikey.GenerateOptions{Owner: "owner-b"})
	require.NoError(t, err)
	require.NoError(t, store.Insert(ctx, other))

	list, err := store.ListByOwner(ctx, "owner-a")
	require.NoError(t, err)
	assert.Len(t, list, 3)
}
