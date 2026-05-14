package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ErrNoTx is returned by [RequireTx] when ctx does not carry an active
// [pgx.Tx]. The outbox.Writer surfaces this via its txCheck so a
// producer that forgets to wrap its write in a transaction fails fast
// rather than silently splitting business state and outbox row across
// two transactions.
var ErrNoTx = errors.New("outbox/postgres: ctx does not carry a pgx.Tx (use WithTx before calling outbox.Writer.Write)")

// txCtxKey is the unexported context key under which [WithTx] stores
// the pgx.Tx. Unexported so external code cannot bypass the WithTx
// helper and stash a non-tx value of the same type by accident.
type txCtxKey struct{}

// WithTx returns a child ctx carrying tx so the outbox Inserter can
// pick it up and write the entry inside the caller's business
// transaction. Returns ctx unchanged when tx is nil.
//
// Wire pattern:
//
//	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
//	defer func() { _ = tx.Rollback(ctx) }()
//	ctx := postgres.WithTx(ctx, tx)
//	// ... business writes inside tx ...
//	if err := outboxWriter.Write(ctx, params); err != nil { return err }
//	return tx.Commit(ctx)
func WithTx(ctx context.Context, tx pgx.Tx) context.Context {
	if tx == nil {
		return ctx
	}
	return context.WithValue(ctx, txCtxKey{}, tx)
}

// TxFromContext returns the pgx.Tx stashed via [WithTx] and a presence
// flag. Useful for adapters that want to compose multiple kit modules
// over the same business transaction.
func TxFromContext(ctx context.Context) (pgx.Tx, bool) {
	if ctx == nil {
		return nil, false
	}
	v, ok := ctx.Value(txCtxKey{}).(pgx.Tx)
	if !ok || v == nil {
		return nil, false
	}
	return v, true
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
