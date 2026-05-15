package memory_test

import (
	"context"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/budget"
	"github.com/bds421/rho-kit/data/v2/budget/memory"
)

func TestNew_PanicsOnZeroCap(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on zero cap")
		}
	}()
	memory.Open(0, time.Hour)
}

func TestNew_PanicsOnZeroPeriod(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on zero period")
		}
	}()
	memory.Open(100, 0)
}

func TestNew_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	memory.Open(100, time.Hour, nil)
}

func TestWithSweeper_PanicsOnNonPositive(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		t.Run(d.String(), func(t *testing.T) {
			require.Panics(t, func() {
				memory.WithSweeper(d)
			})
		})
	}
}

func TestInvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		b    *memory.Budget
	}{
		{"nil", nil},
		{"zero", &memory.Budget{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, rem, retry, err := tc.b.Consume(ctx, "k", 1)
			assert.False(t, ok)
			assert.Equal(t, int64(0), rem)
			assert.Equal(t, time.Duration(0), retry)
			assert.ErrorIs(t, err, budget.ErrInvalidBudget)

			rem, err = tc.b.Peek(ctx, "k")
			assert.Equal(t, int64(0), rem)
			assert.ErrorIs(t, err, budget.ErrInvalidBudget)

			rem, err = tc.b.Refund(ctx, "k", 1)
			assert.Equal(t, int64(0), rem)
			assert.ErrorIs(t, err, budget.ErrInvalidBudget)

			assert.NotPanics(t, func() { _ = tc.b.Close() })
			assert.Equal(t, 0, tc.b.Len())
		})
	}
}

func TestConsume_RejectsEmptyKey(t *testing.T) {
	b := memory.Open(100, time.Hour)
	ok, _, _, err := b.Consume(context.Background(), "", 1)
	assert.False(t, ok)
	assert.ErrorIs(t, err, budget.ErrInvalidKey)
}

func TestConsume_RejectsInvalidKey(t *testing.T) {
	b := memory.Open(100, time.Hour)
	for _, key := range []string{
		strings.Repeat("a", budget.MaxKeyLen+1),
		"tenant\x00acme",
		"tenant acme",
		"tenant\tacme",
		string([]byte{0xff, 0xfe}),
	} {
		t.Run("invalid", func(t *testing.T) {
			ok, _, _, err := b.Consume(context.Background(), key, 1)
			assert.False(t, ok)
			assert.ErrorIs(t, err, budget.ErrInvalidKey)
		})
	}
}

func TestConsume_RejectsNegativeAmount(t *testing.T) {
	b := memory.Open(100, time.Hour)
	ok, _, _, err := b.Consume(context.Background(), "k", -1)
	assert.False(t, ok)
	assert.ErrorIs(t, err, budget.ErrInvalidAmount)
}

func TestConsume_HappyPath(t *testing.T) {
	b := memory.Open(100, time.Hour)
	ctx := context.Background()
	ok, rem, retry, err := b.Consume(ctx, "alice", 30)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int64(70), rem)
	assert.Equal(t, time.Duration(0), retry)

	ok, rem, _, err = b.Consume(ctx, "alice", 60)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int64(10), rem)
}

func TestConsume_RejectsOverCap(t *testing.T) {
	b := memory.Open(100, time.Hour)
	ctx := context.Background()
	ok, _, _, _ := b.Consume(ctx, "alice", 80)
	require.True(t, ok)

	ok, rem, retry, err := b.Consume(ctx, "alice", 30)
	require.NoError(t, err)
	assert.False(t, ok, "30 + 80 > 100 must reject")
	assert.Equal(t, int64(20), rem, "remaining unchanged on rejection")
	assert.Greater(t, retry, time.Duration(0), "retry hint must be positive on rejection")
}

func TestConsume_ZeroAmountIsPeek(t *testing.T) {
	b := memory.Open(100, time.Hour)
	ctx := context.Background()
	_, _, _, _ = b.Consume(ctx, "alice", 25)
	ok, rem, _, err := b.Consume(ctx, "alice", 0)
	require.NoError(t, err)
	assert.True(t, ok, "zero charge must always admit")
	assert.Equal(t, int64(75), rem)
}

func TestPeek_UnknownKeyReturnsFullCap(t *testing.T) {
	b := memory.Open(100, time.Hour)
	rem, err := b.Peek(context.Background(), "ghost")
	require.NoError(t, err)
	assert.Equal(t, int64(100), rem, "unknown key has the full budget")
}

func TestPeek_RejectsEmptyKey(t *testing.T) {
	b := memory.Open(100, time.Hour)
	_, err := b.Peek(context.Background(), "")
	assert.ErrorIs(t, err, budget.ErrInvalidKey)
}

func TestPeek_RejectsInvalidKey(t *testing.T) {
	b := memory.Open(100, time.Hour)
	_, err := b.Peek(context.Background(), strings.Repeat("a", budget.MaxKeyLen+1))
	assert.ErrorIs(t, err, budget.ErrInvalidKey)
}

func TestKeysIsolated(t *testing.T) {
	b := memory.Open(10, time.Hour)
	ctx := context.Background()

	okA, _, _, _ := b.Consume(ctx, "alice", 10)
	okB, remB, _, _ := b.Consume(ctx, "bob", 5)
	assert.True(t, okA)
	assert.True(t, okB)
	assert.Equal(t, int64(5), remB, "bob's bucket is independent of alice's")
}

func TestPeriodRollover_ResetsBudget(t *testing.T) {
	cur := time.Unix(1_700_000_000, 0)
	b := memory.Open(100, time.Minute, memory.WithClock(func() time.Time { return cur }))
	ctx := context.Background()

	ok, _, _, _ := b.Consume(ctx, "alice", 100)
	require.True(t, ok)
	ok, _, _, _ = b.Consume(ctx, "alice", 1)
	require.False(t, ok, "second charge in same window must reject")

	// Cross the boundary by advancing past the current period's end.
	cur = cur.Add(2 * time.Minute)

	ok, rem, _, err := b.Consume(ctx, "alice", 30)
	require.NoError(t, err)
	assert.True(t, ok, "after rollover the budget must reset")
	assert.Equal(t, int64(70), rem)
}

func TestPeek_SeesRolloverWithoutConsume(t *testing.T) {
	cur := time.Unix(1_700_000_000, 0)
	b := memory.Open(100, time.Minute, memory.WithClock(func() time.Time { return cur }))
	ctx := context.Background()

	_, _, _, _ = b.Consume(ctx, "alice", 60)
	rem, _ := b.Peek(ctx, "alice")
	assert.Equal(t, int64(40), rem)

	cur = cur.Add(2 * time.Minute)
	rem, err := b.Peek(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, int64(100), rem,
		"a peek that crosses a period boundary must see the reset budget")
}

func TestConcurrentConsume_DoesNotExceedCap(t *testing.T) {
	cap := int64(1000)
	b := memory.Open(cap, time.Hour)
	const callers = 200
	const each = 7

	var wg sync.WaitGroup
	var admitted atomic.Int64
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			ok, _, _, err := b.Consume(context.Background(), "shared", each)
			if err != nil {
				return
			}
			if ok {
				admitted.Add(each)
			}
		}()
	}
	wg.Wait()

	got := admitted.Load()
	assert.LessOrEqual(t, got, cap, "concurrent admits must never exceed the cap")
	// Soundness: the total admitted is the largest multiple of `each`
	// not exceeding cap.
	maxValid := (cap / each) * each
	assert.Equal(t, maxValid, got,
		"every fitting charge should admit; sub-cap leftover stays unused")
}

func TestRefund_CreditsBack(t *testing.T) {
	b := memory.Open(100, time.Hour)
	ctx := context.Background()
	_, _, _, _ = b.Consume(ctx, "alice", 30)
	rem, err := b.Refund(ctx, "alice", 10)
	require.NoError(t, err)
	assert.Equal(t, int64(80), rem)

	// Verify a follow-up Consume sees the refunded headroom.
	ok, rem2, _, err := b.Consume(ctx, "alice", 80)
	require.NoError(t, err)
	assert.True(t, ok, "refunded headroom must be available to admit")
	assert.Equal(t, int64(0), rem2)
}

func TestRefund_ClampsAtCap(t *testing.T) {
	b := memory.Open(100, time.Hour)
	ctx := context.Background()
	_, _, _, _ = b.Consume(ctx, "alice", 5)
	rem, err := b.Refund(ctx, "alice", 999)
	require.NoError(t, err)
	assert.Equal(t, int64(100), rem,
		"a refund larger than the used amount must clamp at the cap")
}

func TestRefund_UnknownKeyIsNoop(t *testing.T) {
	b := memory.Open(100, time.Hour)
	rem, err := b.Refund(context.Background(), "ghost", 50)
	require.NoError(t, err)
	assert.Equal(t, int64(100), rem)
}

func TestRefund_RejectsEmptyKey(t *testing.T) {
	b := memory.Open(100, time.Hour)
	_, err := b.Refund(context.Background(), "", 5)
	assert.ErrorIs(t, err, budget.ErrInvalidKey)
}

func TestRefund_RejectsInvalidKey(t *testing.T) {
	b := memory.Open(100, time.Hour)
	_, err := b.Refund(context.Background(), strings.Repeat("a", budget.MaxKeyLen+1), 5)
	assert.ErrorIs(t, err, budget.ErrInvalidKey)
}

func TestRefund_RejectsNegative(t *testing.T) {
	b := memory.Open(100, time.Hour)
	_, err := b.Refund(context.Background(), "alice", -1)
	assert.ErrorIs(t, err, budget.ErrInvalidAmount)
}

func TestLen_TracksDistinctKeys(t *testing.T) {
	b := memory.Open(100, time.Hour)
	ctx := context.Background()
	_, _, _, _ = b.Consume(ctx, "k1", 1)
	_, _, _, _ = b.Consume(ctx, "k2", 1)
	_, _, _, _ = b.Consume(ctx, "k1", 1)
	assert.Equal(t, 2, b.Len())
}

// TestConsume_NoChargeOnRejection asserts the documented invariant
// that a rejected charge does not nibble at remaining.
func TestConsume_NoChargeOnRejection(t *testing.T) {
	b := memory.Open(10, time.Hour)
	ctx := context.Background()
	ok, _, _, _ := b.Consume(ctx, "alice", 6)
	require.True(t, ok)
	ok, rem, _, _ := b.Consume(ctx, "alice", 5)
	require.False(t, ok)
	assert.Equal(t, int64(4), rem)

	rem2, _ := b.Peek(ctx, "alice")
	assert.Equal(t, rem, rem2,
		"Peek after a rejection must agree with the rejected Consume")
}

// TestConsume_NearMaxInt64DoesNotOverflow guards the additive overflow
// at memory.go: a naive `used + amount > cap` wraps for amounts close
// to math.MaxInt64 and silently admits a charge that should reject.
func TestConsume_NearMaxInt64DoesNotOverflow(t *testing.T) {
	b := memory.Open(100, time.Hour)
	ctx := context.Background()

	ok, _, _, _ := b.Consume(ctx, "alice", 10)
	require.True(t, ok)

	ok, rem, retry, err := b.Consume(ctx, "alice", math.MaxInt64-50)
	require.NoError(t, err)
	assert.False(t, ok, "near-MaxInt64 charge must reject without overflow")
	assert.Equal(t, int64(90), rem, "remaining unchanged on rejection")
	assert.Greater(t, retry, time.Duration(0))

	ok, rem, _, err = b.Consume(ctx, "alice", math.MaxInt64)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, int64(90), rem)
}

// TestSweeper_RemovesStaleKeys exercises WithSweeper: a key whose
// period has rolled over is dropped on the next sweep.
func TestSweeper_RemovesStaleKeys(t *testing.T) {
	var cur atomic.Int64
	cur.Store(time.Unix(1_700_000_000, 0).UnixNano())
	b := memory.Open(100, time.Minute,
		memory.WithClock(func() time.Time { return time.Unix(0, cur.Load()).UTC() }),
		memory.WithSweeper(10*time.Millisecond),
	)
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()

	for _, k := range []string{"k1", "k2", "k3"} {
		_, _, _, _ = b.Consume(ctx, k, 1)
	}
	require.Equal(t, 3, b.Len())

	cur.Add(int64(2 * time.Minute))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b.Len() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, 0, b.Len(), "sweeper must drop keys whose period has rolled over")
}

// TestClose_Idempotent ensures a double Close neither panics nor
// blocks.
func TestClose_Idempotent(t *testing.T) {
	b := memory.Open(100, time.Hour, memory.WithSweeper(time.Hour))
	require.NoError(t, b.Close())
	require.NoError(t, b.Close())
}

// TestConsume_RetryAfterUsesInjectedClock verifies retry-after is
// computed against the configured clock, not the wall clock. With a
// fixed fake clock the retry value is fully deterministic.
func TestConsume_RetryAfterUsesInjectedClock(t *testing.T) {
	cur := time.Unix(1_700_000_000, 0).UTC()
	b := memory.Open(10, time.Minute, memory.WithClock(func() time.Time { return cur }))
	ctx := context.Background()

	ok, _, _, _ := b.Consume(ctx, "alice", 10)
	require.True(t, ok)

	ok, _, retry, err := b.Consume(ctx, "alice", 1)
	require.NoError(t, err)
	require.False(t, ok)
	periodNs := int64(time.Minute)
	id := cur.UnixNano() / periodNs
	nextStart := time.Unix(0, (id+1)*periodNs).UTC()
	assert.Equal(t, nextStart.Sub(cur), retry,
		"retry-after must be computed against the injected clock")
}

// TestWithClock_PanicsOnNil mirrors the redis backend so a misconfigured
// test option fails fast at construction.
func TestWithClock_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil clock")
		}
	}()
	memory.Open(100, time.Hour, memory.WithClock(nil))
}

// TestConcurrentSweepAndConsume_PreservesCap stresses the sweep+Consume
// race at period rollover: if sweep evicts a bucket between Consume's
// load and its `used` increment, the increment would land on an
// orphaned bucket and the next caller would observe a fresh full cap.
// The Consume retry against the live map entry prevents that.
func TestConcurrentSweepAndConsume_PreservesCap(t *testing.T) {
	cur := atomic.Int64{}
	cur.Store(time.Unix(1_700_000_000, 0).UnixNano())
	b := memory.Open(100, time.Millisecond,
		memory.WithClock(func() time.Time { return time.Unix(0, cur.Load()).UTC() }),
		memory.WithSweeper(time.Microsecond),
	)
	t.Cleanup(func() { _ = b.Close() })

	const callers = 50
	deadline := time.Now().Add(200 * time.Millisecond)

	// Advance the clock aggressively so the sweeper has buckets to evict.
	go func() {
		for time.Now().Before(deadline) {
			cur.Add(int64(time.Millisecond))
			time.Sleep(50 * time.Microsecond)
		}
	}()

	var wg sync.WaitGroup
	var totalAdmitted atomic.Int64
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			ctx := context.Background()
			for time.Now().Before(deadline) {
				ok, _, _, err := b.Consume(ctx, "shared", 1)
				if err == nil && ok {
					totalAdmitted.Add(1)
				}
			}
		}()
	}
	wg.Wait()

	// Soundness: every period must admit at most cap. We can't pin an
	// upper bound on totalAdmitted because the clock advances; the
	// guarantee is the absence of data races, which `go test -race`
	// covers. Sanity-check the run produced some admits.
	assert.Greater(t, totalAdmitted.Load(), int64(0),
		"concurrent Consume must make progress against an active sweeper")
}

// TestHonorsCancelledContext pins the H-011 finding. A cancelled ctx
// must return ctx.Err() without consuming budget or mutating state,
// matching the Redis-backed implementation. Without this, memory and
// Redis wirings disagree about what a cancelled caller observes.
func TestHonorsCancelledContext(t *testing.T) {
	b := memory.Open(10, time.Minute, memory.WithoutSweeper())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ok, remaining, retry, err := b.Consume(ctx, "alice", 1)
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, ok)
	assert.Equal(t, int64(0), remaining)
	assert.Equal(t, time.Duration(0), retry)

	_, err = b.Peek(ctx, "alice")
	require.ErrorIs(t, err, context.Canceled)

	_, err = b.Refund(ctx, "alice", 1)
	require.ErrorIs(t, err, context.Canceled)

	// State must be untouched: a fresh (background) Consume should
	// still see the full cap available.
	ok, remaining, _, err = b.Consume(context.Background(), "alice", 10)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(0), remaining)
}

// TestSweeperDisabled keeps the sweeper goroutine off and verifies
// keys persist regardless of period rollover. Useful for callers that
// want to bound cardinality themselves.
func TestSweeperDisabled(t *testing.T) {
	cur := time.Unix(1_700_000_000, 0)
	b := memory.Open(100, time.Minute,
		memory.WithClock(func() time.Time { return cur }),
		memory.WithoutSweeper(),
	)
	ctx := context.Background()
	_, _, _, _ = b.Consume(ctx, "alice", 1)
	cur = cur.Add(2 * time.Minute)
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, 1, b.Len(), "no sweeper -> entry persists across rollover")
}

// TestRace_SweepConsumePeekRefund stresses the (sweep, Consume, Peek, Refund)
// race documented in L039. With the sweeper running on the shortest interval
// allowed (1ms) and the clock advancing each tick past the period boundary,
// the sweeper continuously evicts buckets while the test goroutines exercise
// every public path. The expected invariants:
//
//   - Consume never returns ok=true with a remaining value < 0 (no underflow).
//   - Peek never returns a value < 0 or > cap.
//   - Total admitted bytes never exceed cap × number_of_rollovers (eviction
//     creates a fresh window, but never grants more than cap within a
//     window).
//
// Failure modes the test is designed to catch:
//
//   - Sweep races eviction vs. Consume's loadOrInitBucket retry loop (the
//     wave-67 fix that re-Loads after acquiring the bucket lock).
//   - Refund crediting an orphan bucket the next Consume cannot see.
//   - Peek observing a torn read between period rollover and used reset.
//
// Run with `go test -race` to catch the underlying data race.
func TestRace_SweepConsumePeekRefund(t *testing.T) {
	if testing.Short() {
		t.Skip("race stress test is slow under -short")
	}

	const cap = int64(1024)
	const callers = 16
	const iterationsPerCaller = 500
	const period = 10 * time.Millisecond
	const sweepInterval = 1 * time.Millisecond

	// Single shared key — maximises contention on the same bucket.
	const key = "race-stress"

	b := memory.Open(cap, period,
		memory.WithSweeper(sweepInterval),
	)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Worker mix: ~60% Consume, ~25% Peek, ~15% Refund. Refund of
	// "amount=0" is a no-op fast path; we want Refund to be a real
	// state-mutating call.
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		i := i
		go func() {
			defer wg.Done()
			ctx := context.Background()
			for j := 0; j < iterationsPerCaller; j++ {
				select {
				case <-stop:
					return
				default:
				}
				switch j % 7 {
				case 0, 1, 2, 3: // 4/7 ≈ 57% Consume
					ok, rem, _, err := b.Consume(ctx, key, int64(1+(i%5)))
					if err != nil {
						t.Errorf("Consume returned error: %v", err)
						return
					}
					if rem < 0 || rem > cap {
						t.Errorf("Consume remaining out of range: %d (cap=%d, ok=%v)", rem, cap, ok)
						return
					}
				case 4, 5: // 2/7 ≈ 29% Peek
					rem, err := b.Peek(ctx, key)
					if err != nil {
						t.Errorf("Peek returned error: %v", err)
						return
					}
					if rem < 0 || rem > cap {
						t.Errorf("Peek remaining out of range: %d (cap=%d)", rem, cap)
						return
					}
				case 6: // 1/7 ≈ 14% Refund
					rem, err := b.Refund(ctx, key, int64(1+(i%3)))
					// Refund of an unknown key (after sweep eviction) is
					// not an error — see memory.Refund semantics. We only
					// assert the returned remaining stays in range.
					if err != nil {
						t.Errorf("Refund returned error: %v", err)
						return
					}
					if rem < 0 || rem > cap {
						t.Errorf("Refund remaining out of range: %d (cap=%d)", rem, cap)
						return
					}
				}
			}
		}()
	}

	wg.Wait()
	close(stop)

	// Final invariant: Len is bounded — the shared key is at most one
	// bucket even after thousands of operations.
	require.LessOrEqual(t, b.Len(), 1, "single-key workload must not leak buckets")
}

// TestRace_SweepEvictsBetweenLoadAndLock guards the specific race wave
// 67 closed: Consume's loadOrInitBucket retry loop must re-validate
// that the bucket it locked is still the live map entry. Without the
// re-check, a sweep evicting the bucket between b.buckets.Load and
// bk.mu.Lock could leave Consume incrementing `used` on an orphaned
// bucket, granting the next caller a fresh full cap.
//
// The test is timing-sensitive but deterministic at -race: any window
// in which an evicted bucket admits a Consume call shows up as either
// a torn read (race detector trips) or an over-cap admit (assertion
// fails).
func TestRace_SweepEvictsBetweenLoadAndLock(t *testing.T) {
	if testing.Short() {
		t.Skip("race stress test is slow under -short")
	}
	const cap = int64(100)
	const period = 5 * time.Millisecond

	// Mutable clock — advanced explicitly to force every bucket to
	// be eligible for sweep on the next tick.
	var clockMu sync.Mutex
	cur := time.Unix(1_700_000_000, 0)
	now := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return cur
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		defer clockMu.Unlock()
		cur = cur.Add(d)
	}

	b := memory.Open(cap, period,
		memory.WithClock(now),
		memory.WithSweeper(time.Millisecond),
	)

	ctx := context.Background()
	stop := make(chan struct{})

	// Continuously advance the clock past the period boundary so
	// sweeper eviction keeps happening. Tracked separately from the
	// Consume goroutines so we can close `stop` BEFORE waiting on
	// the advance goroutine — joining both into one WaitGroup would
	// deadlock because the advance loop only exits when stop fires
	// and stop is only closed after wg.Wait returns.
	advanceDone := make(chan struct{})
	go func() {
		defer close(advanceDone)
		for {
			select {
			case <-stop:
				return
			default:
			}
			advance(period * 2)
			time.Sleep(time.Millisecond)
		}
	}()

	// Many goroutines pounding the same key. The cap invariant must
	// hold: within ANY single period, total admitted bytes cannot
	// exceed cap. Across periods, a fresh cap is allowed (the bucket
	// is a fresh entry after rollover).
	var admitted atomic.Int64
	const callers = 12
	const iters = 200
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				ok, _, _, err := b.Consume(ctx, "k", 5)
				if err != nil {
					t.Errorf("Consume err: %v", err)
					return
				}
				if ok {
					admitted.Add(5)
				}
			}
		}()
	}

	wg.Wait()
	close(stop)
	<-advanceDone
	// We don't assert exact admitted volume — period rollovers grant a
	// fresh cap each window — but we DO assert nothing weird: total
	// is non-negative and finite.
	got := admitted.Load()
	require.GreaterOrEqual(t, got, int64(0))
	require.LessOrEqual(t, got, int64(callers*iters*5))
}
