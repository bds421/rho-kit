package locktest

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/lock"
)

// Factory constructs a fresh Locker for one subtest. It receives
// *testing.T so it can register cleanup via t.Cleanup.
type Factory func(t *testing.T) lock.Locker

// Run executes the full conformance battery against the supplied
// factory.
func Run(t *testing.T, factory Factory) {
	t.Helper()
	if factory == nil {
		t.Fatal("locktest.Run: factory must not be nil")
	}

	t.Run("AcquireOnFreshKeySucceeds", func(t *testing.T) { testAcquireFresh(t, factory) })
	t.Run("AcquireOnHeldKeyReturnsContended", func(t *testing.T) { testAcquireContended(t, factory) })
	t.Run("AcquireAfterReleaseSucceeds", func(t *testing.T) { testAcquireAfterRelease(t, factory) })
	t.Run("DoubleReleaseReturnsErrLockLost", func(t *testing.T) { testDoubleReleaseLost(t, factory) })
	t.Run("ExtendOnHeldLockSucceeds", func(t *testing.T) { testExtendHeld(t, factory) })
	t.Run("ExtendOnReleasedLockReturnsFalse", func(t *testing.T) { testExtendReleased(t, factory) })
	t.Run("DifferentKeysDoNotConflict", func(t *testing.T) { testDifferentKeys(t, factory) })
	t.Run("ConcurrentAcquireExactlyOneWinner", func(t *testing.T) { testConcurrentWinner(t, factory) })
}

func testAcquireFresh(t *testing.T, factory Factory) {
	l := factory(t)
	ctx := context.Background()

	got, ok, err := l.Acquire(ctx, "key-fresh")
	require.NoError(t, err)
	require.True(t, ok, "first Acquire on a fresh key must succeed")
	require.NotNil(t, got, "successful Acquire must return a non-nil Lock")
	require.NoError(t, got.Release(ctx))
}

func testAcquireContended(t *testing.T, factory Factory) {
	l := factory(t)
	ctx := context.Background()
	const key = "key-contended"

	first, ok, err := l.Acquire(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	defer func() { _ = first.Release(ctx) }()

	second, ok, err := l.Acquire(ctx, key)
	require.NoError(t, err, "Acquire on a held lock must not error — contention is normal")
	assert.False(t, ok, "second Acquire of a held key must return ok=false")
	assert.Nil(t, second, "contended Acquire must return a nil Lock handle")
}

func testAcquireAfterRelease(t *testing.T, factory Factory) {
	l := factory(t)
	ctx := context.Background()
	const key = "key-cycle"

	first, ok, err := l.Acquire(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, first.Release(ctx))

	second, ok, err := l.Acquire(ctx, key)
	require.NoError(t, err)
	require.True(t, ok, "after Release, the key is free and Acquire must succeed")
	require.NotNil(t, second)
	require.NoError(t, second.Release(ctx))
}

func testDoubleReleaseLost(t *testing.T, factory Factory) {
	l := factory(t)
	ctx := context.Background()
	const key = "key-double-release"

	got, ok, err := l.Acquire(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, got.Release(ctx), "first Release must succeed")

	err = got.Release(ctx)
	require.Error(t, err, "Release on an already-released Lock must return an error")
	assert.ErrorIs(t, err, lock.ErrLockLost, "the error MUST be lock.ErrLockLost so callers can errors.Is detect it")
}

func testExtendHeld(t *testing.T, factory Factory) {
	l := factory(t)
	ctx := context.Background()
	const key = "key-extend-held"

	got, ok, err := l.Acquire(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	defer func() { _ = got.Release(ctx) }()

	extended, err := got.Extend(ctx)
	require.NoError(t, err, "Extend on a held lock must not error")
	assert.True(t, extended, "Extend on a held lock must return true")
}

func testExtendReleased(t *testing.T, factory Factory) {
	l := factory(t)
	ctx := context.Background()
	const key = "key-extend-released"

	got, ok, err := l.Acquire(ctx, key)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, got.Release(ctx))

	// Extend on a released lock: returns (false, nil) — NOT an
	// error. Losing the race is normal in distributed systems;
	// the caller checks the bool, not the err.
	extended, err := got.Extend(ctx)
	require.NoError(t, err, "Extend on a released lock must not error")
	assert.False(t, extended, "Extend on a released lock must return false")
}

func testDifferentKeys(t *testing.T, factory Factory) {
	l := factory(t)
	ctx := context.Background()

	a, ok, err := l.Acquire(ctx, "key-a")
	require.NoError(t, err)
	require.True(t, ok)
	defer func() { _ = a.Release(ctx) }()

	b, ok, err := l.Acquire(ctx, "key-b")
	require.NoError(t, err, "different keys must not block each other")
	require.True(t, ok, "different keys must not block each other")
	require.NoError(t, b.Release(ctx))
}

func testConcurrentWinner(t *testing.T, factory Factory) {
	l := factory(t)
	ctx := context.Background()
	const key = "key-race"

	var (
		winners    atomic.Int32 // total acquirers that ever held the lock
		inCritical atomic.Int32 // holders currently inside the held window
		maxHolders atomic.Int32 // high-water mark of concurrent holders
	)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, ok, err := l.Acquire(ctx, key)
			if err != nil || !ok || got == nil {
				return
			}
			winners.Add(1)

			// Enter the critical section. The kit's central safety
			// property is "exactly one holder at any instant": this
			// counter must never read above 1. Record the high-water
			// mark so a Locker that grants the lock to every caller
			// is caught.
			held := inCritical.Add(1)
			for {
				prev := maxHolders.Load()
				if held <= prev || maxHolders.CompareAndSwap(prev, held) {
					break
				}
			}

			// Hold briefly so siblings race against a held lock, not
			// the post-release state — this widens the window in
			// which a mutual-exclusion violation would be observed.
			time.Sleep(5 * time.Millisecond)

			inCritical.Add(-1)
			_ = got.Release(ctx)
		}()
	}
	wg.Wait()

	// The race must resolve to at least one winner, and at no instant
	// may two holders share the critical section. Without the
	// maxHolders assertion a Locker that hands the lock to all 16
	// callers simultaneously would pass — that is the bug this guards.
	assert.GreaterOrEqual(t, int(winners.Load()), 1, "at least one acquirer must win")
	assert.LessOrEqual(t, int(winners.Load()), 16, "winners cap matches the goroutine count")
	assert.EqualValues(t, 0, inCritical.Load(), "all holders must have left the critical section")
	assert.LessOrEqual(t, int(maxHolders.Load()), 1,
		"mutual exclusion violated: two or more holders were in the critical section at once")
}
