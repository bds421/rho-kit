//go:build integration

package integrationtest

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

	postgresstore "github.com/bds421/rho-kit/data/approval/postgres/v2"
	"github.com/bds421/rho-kit/data/v2/approval"
	"github.com/bds421/rho-kit/infra/sqldb/dbtest/v2"
)

func startPostgres(t *testing.T) string {
	t.Helper()
	cfg := dbtest.StartPostgres(t, "approval_test")
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

func TestPostgres_Live_Lifecycle(t *testing.T) {
	dsn := startPostgres(t)
	pool := openAndMigrate(t, dsn)
	store := postgresstore.New(pool)

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

	again, err := store.Decide(context.Background(), "r1", "approver-2", "still ok", true)
	require.NoError(t, err)
	assert.Equal(t, approval.StateApproved, again.State)
	assert.Equal(t, "approver-1", again.DecidedBy)
	assert.Equal(t, "ok", again.Reason)
	assert.Equal(t, approved.DecidedAt, again.DecidedAt)

	executed, err := store.MarkExecuted(context.Background(), "r1")
	require.NoError(t, err)
	assert.Equal(t, approval.StateExecuted, executed.State)
}
