package pgstore_test

import (
	"testing"

	"github.com/bds421/rho-kit/data/saga/pgstore/v2"
)

func TestNew_PanicsOnNilDB(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic")
		}
	}()
	_ = pgstore.New(nil)
}

func TestWithTableName_PanicsOnUnsafeIdentifier(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic")
		}
	}()
	_ = pgstore.WithTableName("drop;")
}

// Full SQL-roundtrip tests (Put / Get / ListResumable / Delete /
// optimistic-concurrency) belong under //go:build integration with
// infra/sqldb/dbtest. Unit tier exercises the panic guards above.
