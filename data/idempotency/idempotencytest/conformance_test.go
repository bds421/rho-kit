package idempotencytest_test

import (
	"testing"

	"github.com/bds421/rho-kit/data/v2/idempotency"
	"github.com/bds421/rho-kit/data/v2/idempotency/idempotencytest"
)

// TestMemoryStore_Conformance dogfoods the conformance suite
// against the kit's MemoryStore. Every other Store backend
// (pgstore, redisstore, and any third-party adapter) is expected
// to pass the same battery — drop-in replacement is the whole
// point of the Store interface.
func TestMemoryStore_Conformance(t *testing.T) {
	idempotencytest.Run(t, func(t *testing.T) idempotency.Store {
		return idempotency.NewMemoryStore()
	})
}
