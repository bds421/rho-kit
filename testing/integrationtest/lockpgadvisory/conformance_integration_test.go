//go:build integration

package pgadvisory_test

import (
	"testing"

	kitpgadvisory "github.com/bds421/rho-kit/data/lock/pgadvisory/v2"
	"github.com/bds421/rho-kit/data/v2/lock"
	"github.com/bds421/rho-kit/data/v2/lock/locktest"
)

// TestPgAdvisory_Conformance runs the kit's lock.Locker
// conformance battery against pgadvisory.
//
// The kit's pgadvisory Locker uses session-scoped advisory
// locks, which pin one *sql.DB connection per active lock.
// The conformance suite's ConcurrentAcquire test exercises
// up to 16 simultaneous Acquire calls — the test DB's
// MaxOpenConns must therefore be >= 16 to avoid pool
// deadlock, which is why this factory calls
// SetMaxOpenConns(32) on the DB returned by newTestDB.
func TestPgAdvisory_Conformance(t *testing.T) {
	locktest.Run(t, func(t *testing.T) lock.Locker {
		db := newTestDB(t)
		db.SetMaxOpenConns(32)
		return kitpgadvisory.New(db)
	})
}
