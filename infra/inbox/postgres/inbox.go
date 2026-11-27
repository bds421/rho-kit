package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	outboxpostgres "github.com/bds421/rho-kit/infra/outbox/postgres/v2"
)

// Result is the outcome of one durable delivery claim.
type Result = outboxpostgres.InboxResult

// Handler performs local work within the transaction passed through ctx.
type Handler = outboxpostgres.InboxHandler

// Store is the PostgreSQL transactional inbox implementation.
type Store = outboxpostgres.Inbox

// Option configures a Store.
type Option = outboxpostgres.InboxOption

// WithSchema makes the Store use inbox_entries in schema instead of the
// table search_path resolves; see outbox/postgres.WithInboxSchema.
func WithSchema(schema string) Option { return outboxpostgres.WithInboxSchema(schema) }

// New constructs a durable inbox using the same pgx transaction context as the
// Postgres outbox adapter.
func New(pool *pgxpool.Pool, opts ...Option) *Store { return outboxpostgres.NewInbox(pool, opts...) }

// Process is provided for documentation discoverability on the focused inbox
// import path. It delegates to Store.Process.
func Process(store *Store, ctx context.Context, consumer, messageID string, handler Handler) (Result, error) {
	return store.Process(ctx, consumer, messageID, handler)
}

// ProcessInTx delegates to Store.ProcessInTx for callers owning the wider
// transaction themselves.
func ProcessInTx(store *Store, ctx context.Context, consumer, messageID string, handler Handler) (Result, error) {
	return store.ProcessInTx(ctx, consumer, messageID, handler)
}

// PruneBefore delegates to Store.PruneBefore.
func PruneBefore(store *Store, ctx context.Context, before time.Time) (int64, error) {
	return store.PruneBefore(ctx, before)
}

// Count returns retained receipt count for retention monitoring.
func Count(store *Store, ctx context.Context) (int64, error) { return store.Count(ctx) }
