package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
)

// ErrNoTx is returned by [RequireTx] when ctx does not carry an active
// [pgx.Tx]. The outbox.Writer surfaces this via its txCheck so a
// producer that forgets to wrap its write in a transaction fails fast
// rather than silently splitting business state and outbox row across
// two transactions.
var ErrNoTx = errors.New("outbox/postgres: ctx does not carry a pgx.Tx (use WithTx before calling outbox.Writer.Write)")

// WithTx returns a child ctx carrying tx so the outbox Inserter can
// pick it up and write the entry inside the caller's business
// transaction. Returns ctx unchanged when tx is nil.
//
// It delegates to infra/sqldb/pgx.WithTx and shares its context key, so
// an outbox write inside pgxbackend.Pool.InTx joins that transaction
// automatically, and a tx stored here is visible to Pool.Querier.
//
// Wire pattern:
//
//	err := pool.InTx(ctx, func(ctx context.Context) error {
//	    // ... business writes via pool.Querier(ctx) ...
//	    return outboxWriter.Write(ctx, params)
//	})
func WithTx(ctx context.Context, tx pgx.Tx) context.Context {
	return pgxbackend.WithTx(ctx, tx)
}

// TxFromContext returns the pgx.Tx stashed via [WithTx] (or
// infra/sqldb/pgx.WithTx / Pool.InTx) and a presence flag. Useful for
// adapters that want to compose multiple kit modules over the same
// business transaction.
func TxFromContext(ctx context.Context) (pgx.Tx, bool) {
	return pgxbackend.TxFromContext(ctx)
}

// RequireTx is the [outbox.Writer] txCheck callback: it returns
// [ErrNoTx] when ctx does not carry a tx and nil otherwise. Plug it
// into outbox.NewWriter to enforce "outbox writes must be
// transactional" at the producer boundary.
func RequireTx(ctx context.Context) error {
	if _, ok := TxFromContext(ctx); !ok {
		return ErrNoTx
	}
	return nil
}
