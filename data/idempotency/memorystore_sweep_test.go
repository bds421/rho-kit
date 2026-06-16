package idempotency

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// fixedClock returns a controllable time source for deterministic eviction
// tests. The returned advance closure moves the clock forward so callers can
// age entries past their TTL without time.Sleep.
func fixedClock(start time.Time) (func() time.Time, func(time.Duration)) {
	now := start
	clk := func() time.Time { return now }
	advance := func(d time.Duration) { now = now.Add(d) }
	return clk, advance
}

// TestMemoryStore_SweepExpiredLocked_LocksNotStarvedByItems guards against the
// shared-budget bug: sweepExpiredLocked used a single scan counter spanning the
// items map and the locks map, so once the items loop consumed the whole budget
// the locks loop never ran. A full items map could therefore wedge expired
// locks in memory forever, even though the sweep claims to cover "items +
// locks".
func TestMemoryStore_SweepExpiredLocked_LocksNotStarvedByItems(t *testing.T) {
	clk, advance := fixedClock(time.Unix(0, 0))
	store := NewMemoryStore(WithMemoryStoreClock(clk))

	// Seed more expired items than the eviction budget so a budget-bounded
	// sweep would, under the bug, spend its entire scan allowance on items.
	itemCount := evictBudget + 50
	for i := 0; i < itemCount; i++ {
		store.items[fmt.Sprintf("item-%d", i)] = memEntry{
			resp:      CachedResponse{StatusCode: 200},
			expiresAt: clk(),
		}
	}
	// Seed one expired lock.
	store.locks["abandoned-lock"] = memLock{
		token:     "tok",
		expiresAt: clk(),
	}

	// Age everything past expiry.
	advance(time.Hour)

	store.mu.Lock()
	store.sweepExpiredLocked(evictBudget)
	store.mu.Unlock()

	if _, ok := store.locks["abandoned-lock"]; ok {
		t.Fatalf("expired lock was not swept: items map starved the lock-sweep budget")
	}
}

// TestMemoryStore_TryLock_SweepsAbandonedLocks guards against abandoned locks
// accumulating unbounded. A handler that crashes after TryLock without ever
// calling Set or Unlock leaves an expired lock behind. Nothing on the TryLock
// path cleaned those up, so a churning workload whose handlers never reach Set
// could leak locks indefinitely. TryLock must opportunistically reclaim expired
// locks.
func TestMemoryStore_TryLock_SweepsAbandonedLocks(t *testing.T) {
	clk, advance := fixedClock(time.Unix(0, 0))
	store := NewMemoryStore(WithMemoryStoreClock(clk))
	ctx := context.Background()

	// Many abandoned (soon-to-expire) locks under distinct keys.
	abandoned := evictBudget + 10
	for i := 0; i < abandoned; i++ {
		store.locks[fmt.Sprintf("dead-%d", i)] = memLock{
			token:     "tok",
			expiresAt: clk().Add(time.Second),
		}
	}

	// Age past the abandoned-lock TTL.
	advance(time.Hour)

	// Drive TryLock on fresh keys enough times to trigger opportunistic
	// lock eviction. Each acquires a new live lock for its own key.
	for i := 0; i < tryLockEvictInterval*2; i++ {
		key := fmt.Sprintf("live-%d", i)
		_, _, ok, err := store.TryLock(ctx, key, []byte("fp"), time.Minute)
		if err != nil || !ok {
			t.Fatalf("TryLock(%s): ok=%v err=%v", key, ok, err)
		}
	}

	store.mu.RLock()
	var expiredRemaining int
	for _, l := range store.locks {
		if clk().After(l.expiresAt) {
			expiredRemaining++
		}
	}
	store.mu.RUnlock()

	if expiredRemaining == abandoned {
		t.Fatalf("TryLock never swept any abandoned locks (%d still expired)", expiredRemaining)
	}
}
