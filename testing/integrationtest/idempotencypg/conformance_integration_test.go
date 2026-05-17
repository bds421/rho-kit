//go:build integration

package idempotencypg

import (
	"database/sql"
	"testing"

	"github.com/bds421/rho-kit/data/idempotency/pgstore/v2"
	"github.com/bds421/rho-kit/data/v2/idempotency"
	"github.com/bds421/rho-kit/data/v2/idempotency/idempotencytest"
)

// TestPgStore_Conformance runs the kit's idempotency.Store
// conformance battery (wave 178) against pgstore. If pgstore
// ever diverges from the MemoryStore-validated contract, this
// fails before tag. Each subtest gets a fresh table-cleared
// database so per-key state doesn't bleed across cases.
func TestPgStore_Conformance(t *testing.T) {
	idempotencytest.Run(t, func(t *testing.T) idempotency.Store {
		db := testDB(t)
		clearIdempotencyTable(t, db)
		return pgstore.New(db)
	})
}

// clearIdempotencyTable truncates the test table so each
// conformance subtest starts from an empty store. Without this,
// a "Acquire on fresh key succeeds" test could see a leftover
// lock from the previous subtest.
func clearIdempotencyTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec("TRUNCATE TABLE idempotency_keys"); err != nil {
		t.Fatalf("truncate idempotency_keys: %v", err)
	}
}
