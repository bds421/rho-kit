package memory_test

import (
	"context"
	"math"
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
	memory.New(0, time.Hour)
}

func TestNew_PanicsOnZeroPeriod(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on zero period")
		}
	}()
	memory.New(100, 0)
}

func TestConsume_RejectsEmptyKey(t *testing.T) {
	b := memory.New(100, time.Hour)
	ok, _, _, err := b.Consume(context.Background(), "", 1)
	assert.False(t, ok)
	assert.ErrorIs(t, err, budget.ErrInvalidKey)
}

func TestConsume_RejectsNegativeAmount(t *testing.T) {
	b := memory.New(100, time.Hour)
	ok, _, _, err := b.Consume(context.Background(), "k", -1)
	assert.False(t, ok)
	assert.ErrorIs(t, err, budget.ErrInvalidAmount)
}

func TestConsume_HappyPath(t *testing.T) {
	b := memory.New(100, time.Hour)
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
	b := memory.New(100, time.Hour)
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
	b := memory.New(100, time.Hour)
	ctx := context.Background()
	_, _, _, _ = b.Consume(ctx, "alice", 25)
	ok, rem, _, err := b.Consume(ctx, "alice", 0)
	require.NoError(t, err)
	assert.True(t, ok, "zero charge must always admit")
	assert.Equal(t, int64(75), rem)
}

func TestPeek_UnknownKeyReturnsFullCap(t *testing.T) {
	b := memory.New(100, time.Hour)
	rem, err := b.Peek(context.Background(), "ghost")
	require.NoError(t, err)
	assert.Equal(t, int64(100), rem, "unknown key has the full budget")
}

func TestPeek_RejectsEmptyKey(t *testing.T) {
	b := memory.New(100, time.Hour)
	_, err := b.Peek(context.Background(), "")
	assert.ErrorIs(t, err, budget.ErrInvalidKey)
}

func TestKeysIsolated(t *testing.T) {
	b := memory.New(10, time.Hour)
	ctx := context.Background()

	okA, _, _, _ := b.Consume(ctx, "alice", 10)
	okB, remB, _, _ := b.Consume(ctx, "bob", 5)
	assert.True(t, okA)
	assert.True(t, okB)
	assert.Equal(t, int64(5), remB, "bob's bucket is independent of alice's")
}

func TestPeriodRollover_ResetsBudget(t *testing.T) {
	cur := time.Unix(1_700_000_000, 0)
	b := memory.New(100, time.Minute, memory.WithClock(func() time.Time { return cur }))
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
	b := memory.New(100, time.Minute, memory.WithClock(func() time.Time { return cur }))
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
	b := memory.New(cap, time.Hour)
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
	b := memory.New(100, time.Hour)
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
	b := memory.New(100, time.Hour)
	ctx := context.Background()
	_, _, _, _ = b.Consume(ctx, "alice", 5)
	rem, err := b.Refund(ctx, "alice", 999)
	require.NoError(t, err)
	assert.Equal(t, int64(100), rem,
		"a refund larger than the used amount must clamp at the cap")
}

func TestRefund_UnknownKeyIsNoop(t *testing.T) {
	b := memory.New(100, time.Hour)
	rem, err := b.Refund(context.Background(), "ghost", 50)
	require.NoError(t, err)
	assert.Equal(t, int64(100), rem)
}

func TestRefund_RejectsEmptyKey(t *testing.T) {
	b := memory.New(100, time.Hour)
	_, err := b.Refund(context.Background(), "", 5)
	assert.ErrorIs(t, err, budget.ErrInvalidKey)
}

func TestRefund_RejectsNegative(t *testing.T) {
	b := memory.New(100, time.Hour)
	_, err := b.Refund(context.Background(), "alice", -1)
	assert.ErrorIs(t, err, budget.ErrInvalidAmount)
}

func TestLen_TracksDistinctKeys(t *testing.T) {
	b := memory.New(100, time.Hour)
	ctx := context.Background()
	_, _, _, _ = b.Consume(ctx, "k1", 1)
	_, _, _, _ = b.Consume(ctx, "k2", 1)
	_, _, _, _ = b.Consume(ctx, "k1", 1)
	assert.Equal(t, 2, b.Len())
}

// TestConsume_NoChargeOnRejection asserts the documented invariant
// that a rejected charge does not nibble at remaining.
func TestConsume_NoChargeOnRejection(t *testing.T) {
	b := memory.New(10, time.Hour)
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
	b := memory.New(100, time.Hour)
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
	cur := time.Unix(1_700_000_000, 0)
	b := memory.New(100, time.Minute,
		memory.WithClock(func() time.Time { return cur }),
		memory.WithSweeper(10*time.Millisecond),
	)
	t.Cleanup(b.Stop)
	ctx := context.Background()

	for _, k := range []string{"k1", "k2", "k3"} {
		_, _, _, _ = b.Consume(ctx, k, 1)
	}
	require.Equal(t, 3, b.Len())

	cur = cur.Add(2 * time.Minute)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if b.Len() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, 0, b.Len(), "sweeper must drop keys whose period has rolled over")
}

// TestStop_Idempotent ensures a double Stop neither panics nor
// blocks.
func TestStop_Idempotent(t *testing.T) {
	b := memory.New(100, time.Hour, memory.WithSweeper(time.Hour))
	b.Stop()
	b.Stop()
}

// TestConsume_RetryAfterUsesInjectedClock verifies retry-after is
// computed against the configured clock, not the wall clock. With a
// fixed fake clock the retry value is fully deterministic.
func TestConsume_RetryAfterUsesInjectedClock(t *testing.T) {
	cur := time.Unix(1_700_000_000, 0).UTC()
	b := memory.New(10, time.Minute, memory.WithClock(func() time.Time { return cur }))
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
	memory.New(100, time.Hour, memory.WithClock(nil))
}

// TestSweeperDisabled keeps the sweeper goroutine off and verifies
// keys persist regardless of period rollover. Useful for callers that
// want to bound cardinality themselves.
func TestSweeperDisabled(t *testing.T) {
	cur := time.Unix(1_700_000_000, 0)
	b := memory.New(100, time.Minute,
		memory.WithClock(func() time.Time { return cur }),
		memory.WithSweeper(0),
	)
	ctx := context.Background()
	_, _, _, _ = b.Consume(ctx, "alice", 1)
	cur = cur.Add(2 * time.Minute)
	time.Sleep(20 * time.Millisecond)
	assert.Equal(t, 1, b.Len(), "no sweeper -> entry persists across rollover")
}
