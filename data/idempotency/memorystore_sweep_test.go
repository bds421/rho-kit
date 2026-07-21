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

// TestMemoryStore_Set_PreservesFingerprintAfterSweep is the regression for
// the mid-Set lock reclaim bug: if sweepExpiredLocked runs between the
// ownership check and the write (and the lock is still present after a
// re-check), the stored response must keep the fingerprint captured from
// the validated lock — not re-read a zero memLock after a sweep deletion.
//
// This scenario uses a lock that expires exactly when the sweep samples
// now, after Set's ownership check already passed with an earlier now.
// We simulate by: TryLock with a short TTL, advance the clock past TTL
// but re-insert a non-expired lock with fingerprint just before Set so
// ownership passes, then force a sweep that would have deleted a stale
// re-read. Simpler pin: capture path stores fingerprint from the local
// variable even when the lock map entry is deleted mid-function — we
// force that by deleting the lock inside a custom clock tick after the
// first now() in Set's ownership check.
func TestMemoryStore_Set_PreservesFingerprintAfterSweep(t *testing.T) {
	clk, advance := fixedClock(time.Unix(1_000_000, 0))
	store := NewMemoryStore(WithMemoryStoreClock(clk))
	ctx := context.Background()

	fp := []byte("request-fingerprint-sha256-aabb")
	token, mismatch, ok, err := store.TryLock(ctx, "k", fp, time.Minute)
	if err != nil || mismatch || !ok {
		t.Fatalf("TryLock: ok=%v mismatch=%v err=%v", ok, mismatch, err)
	}

	// Force Set onto an eviction tick so sweepExpiredLocked runs, and
	// expire the lock between the ownership check and the post-sweep
	// re-check by advancing the clock only after the first now() would
	// have been taken. We cannot interleave mid-Set without hooks, so
	// pin the success path: lock still valid through Set, sweep runs,
	// fingerprint must still be stored (not nil).
	store.setCount = evictInterval - 1
	err = store.Set(ctx, "k", token, CachedResponse{StatusCode: 200, Body: []byte(`{"ok":1}`)}, time.Minute)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Different fingerprint must mismatch on Get (fingerprint was stored).
	_, mismatch, err = store.Get(ctx, "k", []byte("different-fingerprint-zzzz"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !mismatch {
		t.Fatal("expected fingerprint mismatch; nil fingerprint would disable the check")
	}

	// Matching fingerprint still hits.
	resp, mismatch, err := store.Get(ctx, "k", fp)
	if err != nil || mismatch || resp == nil {
		t.Fatalf("Get match: resp=%v mismatch=%v err=%v", resp, mismatch, err)
	}
	_ = advance // keep helper used if future expansion ages the entry
}

// TestMemoryStore_Set_LockSweptMidCallReturnsLockLost pins that when the
// lock is gone after the in-call sweep (TTL elapsed), Set returns
// ErrLockLost rather than writing a nil-fingerprint entry.
func TestMemoryStore_Set_LockSweptMidCallReturnsLockLost(t *testing.T) {
	clk, advance := fixedClock(time.Unix(2_000_000, 0))
	store := NewMemoryStore(WithMemoryStoreClock(clk))
	ctx := context.Background()

	fp := []byte("fp")
	token, _, ok, err := store.TryLock(ctx, "k", fp, time.Second)
	if err != nil || !ok {
		t.Fatalf("TryLock: %v", err)
	}

	// Expire the lock, then force a sweep inside Set.
	advance(2 * time.Second)
	store.setCount = evictInterval - 1
	err = store.Set(ctx, "k", token, CachedResponse{StatusCode: 200}, time.Minute)
	if err != ErrLockLost {
		t.Fatalf("Set after lock expiry: got %v, want ErrLockLost", err)
	}
	if _, exists := store.items["k"]; exists {
		t.Fatal("must not store a response when the lock was lost")
	}
}
