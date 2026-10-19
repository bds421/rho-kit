package pgx

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Querier is the query surface shared by a pool and a transaction. Its
// method set is identical to the DBTX interface sqlc generates for pgx/v5,
// so generated query code accepts the value returned by [Pool.Querier].
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// txCtxKey is the context key under which [WithTx] stores the transaction.
// infra/outbox/postgres delegates to this package, so repositories and the
// outbox writer see the same transaction.
type txCtxKey struct{}

// WithTx returns a child ctx carrying tx so repositories and kit stores
// (for example infra/outbox/postgres) run inside it. Returns ctx unchanged
// when tx is nil. Most callers use [Pool.InTx] instead.
func WithTx(ctx context.Context, tx pgx.Tx) context.Context {
	if tx == nil {
		return ctx
	}
	return context.WithValue(ctx, txCtxKey{}, tx)
}

// TxFromContext returns the transaction stored by [WithTx] and whether one
// is present.
func TxFromContext(ctx context.Context) (pgx.Tx, bool) {
	if ctx == nil {
		return nil, false
	}
	tx, ok := ctx.Value(txCtxKey{}).(pgx.Tx)
	if !ok || tx == nil {
		return nil, false
	}
	return tx, true
}

// Querier returns the transaction carried by ctx, or the pool when ctx has
// none. Repositories call it per operation so the same code runs inside and
// outside [Pool.InTx].
func (p *Pool) Querier(ctx context.Context) Querier {
	if tx, ok := TxFromContext(ctx); ok {
		return tx
	}
	return p.pool
}

// InTx runs fn inside a transaction carried by the ctx passed to fn (see
// [WithTx]). It commits when fn returns nil and rolls back when fn returns
// an error or panics; a panic is re-raised after the rollback. Commit and
// begin errors are redacted; fn's error is returned unchanged.
//
// When ctx already carries a transaction, fn runs inside it (it joins; no
// savepoint is created) and the outer owner decides commit or rollback.
func (p *Pool) InTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if fn == nil {
		return errors.New("pgx: InTx requires a non-nil function")
	}
	if _, ok := TxFromContext(ctx); ok {
		return fn(ctx)
	}
	if p == nil || p.pool == nil {
		return errors.New("pgx: InTx on a closed or uninitialised pool")
	}
	return runInTx(ctx, func(ctx context.Context) (pgx.Tx, error) {
		return p.pool.Begin(ctx)
	}, fn)
}

// runInTx owns one transaction lifecycle; split from InTx for unit tests.
func runInTx(ctx context.Context, begin func(context.Context) (pgx.Tx, error), fn func(context.Context) error) (err error) {
	tx, err := begin(ctx)
	if err != nil {
		return redact.WrapError("pgx: begin transaction", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		// Roll back even when ctx was canceled, so the connection is
		// released in a clean state.
		rbErr := tx.Rollback(context.WithoutCancel(ctx))
		if r := recover(); r != nil {
			panic(r)
		}
		if rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, redact.WrapError("pgx: rollback transaction", rbErr))
		}
	}()
	if err = fn(WithTx(ctx, tx)); err != nil {
		return err
	}
	if cErr := tx.Commit(ctx); cErr != nil {
		// pgx closes the transaction on a failed commit; the deferred
		// Rollback reports ErrTxClosed and is ignored.
		return redact.WrapError("pgx: commit transaction", cErr)
	}
	committed = true
	return nil
}
