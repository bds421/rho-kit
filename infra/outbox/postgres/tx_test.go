package postgres

import (
	"context"
	"errors"
	"testing"

	pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
)

// TestTxContextSharedWithPgxBackend proves the outbox helpers and
// infra/sqldb/pgx use one context key, so outbox writes inside
// pgxbackend.Pool.InTx join that transaction.
func TestTxContextSharedWithPgxBackend(t *testing.T) {
	tx := &fakeTx{}
	if got, ok := TxFromContext(pgxbackend.WithTx(context.Background(), tx)); !ok || got != tx {
		t.Fatal("outbox TxFromContext must see a tx stored by pgxbackend.WithTx")
	}
	if got, ok := pgxbackend.TxFromContext(WithTx(context.Background(), tx)); !ok || got != tx {
		t.Fatal("pgxbackend.TxFromContext must see a tx stored by outbox WithTx")
	}
	if err := RequireTx(pgxbackend.WithTx(context.Background(), tx)); err != nil {
		t.Fatalf("RequireTx inside a pgxbackend tx = %v", err)
	}
	if err := RequireTx(context.Background()); !errors.Is(err, ErrNoTx) {
		t.Fatalf("RequireTx without tx = %v, want ErrNoTx", err)
	}
	if ctx := context.Background(); WithTx(ctx, nil) != ctx {
		t.Fatal("WithTx(nil) must return ctx unchanged")
	}
}
