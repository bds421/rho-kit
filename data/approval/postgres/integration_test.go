//go:build integration

package postgres

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/bds421/rho-kit/data/approval"
)

func startPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = testcontainers.TerminateContainer(c) })
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return dsn
}

func openAndMigrate(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })

	sub, err := fs.Sub(Migrations, "migrations")
	require.NoError(t, err)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	require.NoError(t, err)
	_, err = provider.Up(ctx)
	require.NoError(t, err)
	return pool
}

func TestPostgres_Live_Lifecycle(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := New(pool)

	r, err := store.Create(context.Background(), approval.Request{
		ID:        "r1",
		TenantID:  "tenant-1",
		Actor:     "agent-7",
		Action:    "user.delete",
		Resource:  "users/42",
		Payload:   []byte(`{"force":true}`),
		ExpiresAt: time.Now().Add(time.Hour),
	})
	require.NoError(t, err)
	assert.Equal(t, approval.StatePending, r.State)

	approved, err := store.Decide(context.Background(), "r1", "approver-1", "ok", true)
	require.NoError(t, err)
	assert.Equal(t, approval.StateApproved, approved.State)

	executed, err := store.MarkExecuted(context.Background(), "r1")
	require.NoError(t, err)
	assert.Equal(t, approval.StateExecuted, executed.State)
}
