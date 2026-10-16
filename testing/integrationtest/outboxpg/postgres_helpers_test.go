//go:build integration

package outboxpg

import (
	"context"
	"encoding/json"
	"io/fs"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/id"
	outboxpg "github.com/bds421/rho-kit/infra/outbox/postgres/v2"
	"github.com/bds421/rho-kit/infra/v2/outbox"
)

func openAndMigrate(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	sqlDB := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = sqlDB.Close() })

	sub, err := fs.Sub(outboxpg.Migrations, "migrations")
	require.NoError(t, err)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	require.NoError(t, err)
	_, err = provider.Up(ctx)
	require.NoError(t, err)
	return pool
}

func mustEntry() outbox.Entry {
	return outbox.Entry{
		ID:          uuid.UUID(id.NewBytes()),
		Topic:       "events",
		RoutingKey:  "user.created",
		MessageID:   "msg-" + uuid.NewString(),
		MessageType: "UserCreated",
		Payload:     json.RawMessage(`{"id":1}`),
		Status:      outbox.StatusPending,
		CreatedAt:   time.Now().UTC(),
	}
}
