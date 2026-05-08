package redis_test

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/budget"
	budgetredis "github.com/bds421/rho-kit/data/budget/redis/v2"
)

func newTestClient(t *testing.T) (goredis.UniversalClient, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client, mr
}

func TestNew_PanicsOnNilClient(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil client")
		}
	}()
	budgetredis.New(nil, 100, time.Hour)
}

func TestNew_PanicsOnZeroCap(t *testing.T) {
	client, _ := newTestClient(t)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on zero cap")
		}
	}()
	budgetredis.New(client, 0, time.Hour)
}

func TestNew_PanicsOnZeroPeriod(t *testing.T) {
	client, _ := newTestClient(t)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on zero period")
		}
	}()
	budgetredis.New(client, 100, 0)
}

func TestNew_PanicsOnTTLLessThanPeriod(t *testing.T) {
	client, _ := newTestClient(t)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on TTL < period")
		}
	}()
	budgetredis.New(client, 100, time.Hour, budgetredis.WithKeyTTL(time.Minute))
}

func TestConsume_RejectsEmptyKey(t *testing.T) {
	client, _ := newTestClient(t)
	b := budgetredis.New(client, 100, time.Hour)
	ok, _, _, err := b.Consume(context.Background(), "", 1)
	assert.False(t, ok)
	assert.ErrorIs(t, err, budget.ErrInvalidKey)
}

func TestConsume_RejectsNegativeAmount(t *testing.T) {
	client, _ := newTestClient(t)
	b := budgetredis.New(client, 100, time.Hour)
	ok, _, _, err := b.Consume(context.Background(), "k", -1)
	assert.False(t, ok)
	assert.ErrorIs(t, err, budget.ErrInvalidAmount)
}

func TestConsume_HappyPath(t *testing.T) {
	client, _ := newTestClient(t)
	b := budgetredis.New(client, 100, time.Hour)
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
	client, _ := newTestClient(t)
	b := budgetredis.New(client, 100, time.Hour)
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
	client, _ := newTestClient(t)
	b := budgetredis.New(client, 100, time.Hour)
	ctx := context.Background()
	_, _, _, _ = b.Consume(ctx, "alice", 25)
	ok, rem, _, err := b.Consume(ctx, "alice", 0)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, int64(75), rem)
}

func TestPeek_UnknownKeyReturnsFullCap(t *testing.T) {
	client, _ := newTestClient(t)
	b := budgetredis.New(client, 100, time.Hour)
	rem, err := b.Peek(context.Background(), "ghost")
	require.NoError(t, err)
	assert.Equal(t, int64(100), rem)
}

func TestPeek_RejectsEmptyKey(t *testing.T) {
	client, _ := newTestClient(t)
	b := budgetredis.New(client, 100, time.Hour)
	_, err := b.Peek(context.Background(), "")
	assert.ErrorIs(t, err, budget.ErrInvalidKey)
}

func TestKeysIsolated(t *testing.T) {
	client, _ := newTestClient(t)
	b := budgetredis.New(client, 10, time.Hour)
	ctx := context.Background()

	okA, _, _, _ := b.Consume(ctx, "alice", 10)
	okB, remB, _, _ := b.Consume(ctx, "bob", 5)
	assert.True(t, okA)
	assert.True(t, okB)
	assert.Equal(t, int64(5), remB)
}

func TestPeriodRollover_ResetsBudget(t *testing.T) {
	client, _ := newTestClient(t)
	cur := time.Unix(1_700_000_000, 0)
	b := budgetredis.New(client, 100, time.Minute,
		budgetredis.WithClock(func() time.Time { return cur }))
	ctx := context.Background()

	ok, _, _, _ := b.Consume(ctx, "alice", 100)
	require.True(t, ok)
	ok, _, _, _ = b.Consume(ctx, "alice", 1)
	require.False(t, ok)

	// Cross the boundary by jumping past the current period.
	cur = cur.Add(2 * time.Minute)

	ok, rem, _, err := b.Consume(ctx, "alice", 30)
	require.NoError(t, err)
	assert.True(t, ok, "after rollover the budget must reset")
	assert.Equal(t, int64(70), rem)
}

func TestAcrossClientsSharesState(t *testing.T) {
	// Whole point of the redis backend: budget applies across replicas.
	mr := miniredis.RunT(t)
	clientA := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	clientB := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = clientA.Close(); _ = clientB.Close() })

	cur := time.Unix(1_700_000_000, 0)
	clk := budgetredis.WithClock(func() time.Time { return cur })
	a := budgetredis.New(clientA, 10, time.Hour, clk)
	b := budgetredis.New(clientB, 10, time.Hour, clk)

	ctx := context.Background()
	okA, _, _, _ := a.Consume(ctx, "shared", 7)
	okB, remB, _, _ := b.Consume(ctx, "shared", 5)
	assert.True(t, okA)
	assert.False(t, okB, "second instance must observe the first's debit and reject 5 against remaining 3")
	assert.Equal(t, int64(3), remB, "remaining reported from B reflects shared state")

	// And a fitting charge from B succeeds.
	okB2, remB2, _, _ := b.Consume(ctx, "shared", 3)
	assert.True(t, okB2)
	assert.Equal(t, int64(0), remB2)
}

func TestConsume_ConcurrentSameKeyDoesNotExceedCap(t *testing.T) {
	// With cap=N, exactly N units worth of admits should happen
	// against a 1-amount call; the rest must reject.
	client, _ := newTestClient(t)
	cap := int64(10)
	b := budgetredis.New(client, cap, time.Hour)

	const callers = 50
	var wg sync.WaitGroup
	var ok atomic.Int64
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			admitted, _, _, err := b.Consume(context.Background(), "shared", 1)
			if err != nil {
				return
			}
			if admitted {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, cap, ok.Load(), "exactly cap admits at the same instant")
}

func TestPeek_ReflectsConsume(t *testing.T) {
	client, _ := newTestClient(t)
	b := budgetredis.New(client, 100, time.Hour)
	ctx := context.Background()

	_, _, _, _ = b.Consume(ctx, "alice", 25)
	rem, err := b.Peek(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, int64(75), rem)
}

func TestKeyPrefix_IsolatesLogicalBudgets(t *testing.T) {
	client, _ := newTestClient(t)
	a := budgetredis.New(client, 5, time.Hour, budgetredis.WithKeyPrefix("a:"))
	b := budgetredis.New(client, 5, time.Hour, budgetredis.WithKeyPrefix("b:"))
	ctx := context.Background()

	okA, _, _, _ := a.Consume(ctx, "tenant", 5)
	okB, _, _, _ := b.Consume(ctx, "tenant", 5)
	assert.True(t, okA)
	assert.True(t, okB,
		"two budgets with the same key but different prefixes must not collide")
}

func TestRefund_CreditsBack(t *testing.T) {
	client, _ := newTestClient(t)
	b := budgetredis.New(client, 100, time.Hour)
	ctx := context.Background()

	_, _, _, _ = b.Consume(ctx, "alice", 30)
	rem, err := b.Refund(ctx, "alice", 10)
	require.NoError(t, err)
	assert.Equal(t, int64(80), rem)

	rem2, _ := b.Peek(ctx, "alice")
	assert.Equal(t, rem, rem2, "Peek must agree with the refunded value")
}

func TestRefund_ClampsAtCap(t *testing.T) {
	client, _ := newTestClient(t)
	b := budgetredis.New(client, 100, time.Hour)
	ctx := context.Background()

	_, _, _, _ = b.Consume(ctx, "alice", 5)
	rem, err := b.Refund(ctx, "alice", 999)
	require.NoError(t, err)
	assert.Equal(t, int64(100), rem)
}

func TestRefund_UnknownKeyIsNoop(t *testing.T) {
	client, _ := newTestClient(t)
	b := budgetredis.New(client, 100, time.Hour)
	rem, err := b.Refund(context.Background(), "ghost", 50)
	require.NoError(t, err)
	assert.Equal(t, int64(100), rem)
}

func TestRefund_RejectsEmptyKey(t *testing.T) {
	client, _ := newTestClient(t)
	b := budgetredis.New(client, 100, time.Hour)
	_, err := b.Refund(context.Background(), "", 5)
	assert.ErrorIs(t, err, budget.ErrInvalidKey)
}

func TestRefund_RejectsNegative(t *testing.T) {
	client, _ := newTestClient(t)
	b := budgetredis.New(client, 100, time.Hour)
	_, err := b.Refund(context.Background(), "alice", -1)
	assert.ErrorIs(t, err, budget.ErrInvalidAmount)
}

func TestRedisTime_Smoke(t *testing.T) {
	// We can't validate the wall-clock value miniredis returns, but
	// we can validate the option compiles, executes, and admits.
	client, _ := newTestClient(t)
	b := budgetredis.New(client, 5, time.Hour, budgetredis.WithRedisTime())
	ok, _, _, err := b.Consume(context.Background(), "alice", 1)
	require.NoError(t, err)
	assert.True(t, ok)
}

// TestConsume_RejectedFirstRequestLeavesNoKey guards against the
// INCRBY-then-DECRBY pattern leaving a non-expiring zero-valued key
// when a brand-new request charges over the cap. miniredis exposes
// TTL inspection so we can assert no orphaned key remains.
func TestConsume_RejectedFirstRequestLeavesNoKey(t *testing.T) {
	client, mr := newTestClient(t)
	b := budgetredis.New(client, 100, time.Hour)
	ctx := context.Background()

	ok, _, _, err := b.Consume(ctx, "alice", 200)
	require.NoError(t, err)
	require.False(t, ok, "200 over cap=100 must reject")

	keys := mr.Keys()
	assert.Empty(t, keys, "rejected first request must not leave a persistent key")
}

// TestConsume_RejectedFollowupRefreshesTTL: when an existing bucket
// rejects a charge the TTL must still be refreshed so the bucket
// continues to expire as expected. We can only assert that an EXPIRE
// is set; the exact TTL is implementation-defined.
func TestConsume_RejectedFollowupRefreshesTTL(t *testing.T) {
	client, mr := newTestClient(t)
	b := budgetredis.New(client, 100, time.Hour)
	ctx := context.Background()

	ok, _, _, err := b.Consume(ctx, "alice", 50)
	require.NoError(t, err)
	require.True(t, ok)

	keys := mr.Keys()
	require.Len(t, keys, 1, "admitted request creates exactly one key")
	ttlBefore := mr.TTL(keys[0])
	require.Greater(t, ttlBefore, time.Duration(0))

	mr.FastForward(30 * time.Minute)

	// Charge 80 against remaining 50: Lua-side rejection (amount <= cap
	// so the Go-side over-cap check does not short-circuit). Lua
	// refreshes the TTL on the existing bucket.
	ok, _, _, err = b.Consume(ctx, "alice", 80)
	require.NoError(t, err)
	require.False(t, ok)

	ttlAfter := mr.TTL(keys[0])
	assert.Greater(t, ttlAfter, time.Duration(0), "TTL must remain set after rejection")
	assert.Greater(t, ttlAfter, 30*time.Minute, "rejected request refreshes TTL")
}

// TestWithClock_PanicsOnNil ensures a misconfigured test option fails
// fast at construction rather than panicking on the first Consume.
func TestWithClock_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil clock")
		}
	}()
	_ = budgetredis.WithClock(nil)
}

// TestConsume_NearMaxInt64DoesNotOverflow guards the Lua script against
// amounts close to math.MaxInt64. Without the pre-INCRBY headroom check
// (and the Go-side amount > cap rejection) Redis would surface a script
// integer overflow error instead of a clean denial.
func TestConsume_NearMaxInt64DoesNotOverflow(t *testing.T) {
	client, _ := newTestClient(t)
	b := budgetredis.New(client, 100, time.Hour)
	ctx := context.Background()

	ok, _, _, _ := b.Consume(ctx, "alice", 10)
	require.True(t, ok)

	ok, rem, retry, err := b.Consume(ctx, "alice", math.MaxInt64-50)
	require.NoError(t, err, "near-MaxInt64 charge must not surface a script error")
	assert.False(t, ok, "charge larger than cap must reject")
	assert.Equal(t, int64(90), rem, "remaining unchanged on rejection")
	assert.Greater(t, retry, time.Duration(0))

	ok, rem, _, err = b.Consume(ctx, "alice", math.MaxInt64)
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, int64(90), rem)
}

// TestRetryAfter_UsesSameTimeSource verifies that retry-after uses
// the configured clock (not local time). With WithClock pinned and
// the local clock free-running, retry-after must equal time-to-next
// boundary measured against the pinned clock.
func TestRetryAfter_UsesSameTimeSource(t *testing.T) {
	client, _ := newTestClient(t)
	period := time.Minute

	cur := time.Unix(1_700_000_000, 0)
	expectedNext := time.Unix(0, ((cur.UTC().UnixNano()/int64(period))+1)*int64(period)).UTC()
	expectedRetry := expectedNext.Sub(cur)

	b := budgetredis.New(client, 10, period,
		budgetredis.WithClock(func() time.Time { return cur }),
	)
	ctx := context.Background()

	ok, _, _, _ := b.Consume(ctx, "alice", 10)
	require.True(t, ok)

	ok, _, retry, err := b.Consume(ctx, "alice", 5)
	require.NoError(t, err)
	require.False(t, ok)
	assert.InDelta(t, float64(expectedRetry), float64(retry), float64(time.Millisecond),
		"retry-after must be derived from the pinned clock, not the host clock")
}
