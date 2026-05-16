package tokenbucket

import (
	"context"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/ratelimit"
)

func TestImplementsLimiter(t *testing.T) {
	var _ ratelimit.Limiter = (*Limiter)(nil)
}

func TestAllow_RejectsEmptyKey(t *testing.T) {
	l := New(5, 1)
	allowed, _, err := l.Allow(context.Background(), "")
	assert.False(t, allowed)
	assert.ErrorIs(t, err, ratelimit.ErrInvalidKey)
}

func TestAllow_RejectsInvalidKey(t *testing.T) {
	l := New(5, 1)

	cases := []string{
		"tenant\nid",
		"tenant\rid",
		"tenant\x00id",
		string([]byte{'t', 'e', 'n', 0xff}),
		strings.Repeat("a", ratelimit.MaxKeyLen+1),
	}
	for _, key := range cases {
		allowed, retry, err := l.Allow(context.Background(), key)
		assert.False(t, allowed)
		assert.Zero(t, retry)
		assert.ErrorIs(t, err, ratelimit.ErrInvalidKey)
	}
}

func TestAllow_FullBucketAcceptsBurst(t *testing.T) {
	l := New(5, 1) // 5 tokens, refill 1/s
	for i := 0; i < 5; i++ {
		ok, _, err := l.Allow(context.Background(), "k")
		require.NoError(t, err)
		assert.True(t, ok, "burst slot %d should be allowed", i+1)
	}
	// 6th in the same instant → denied.
	ok, retry, err := l.Allow(context.Background(), "k")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.True(t, retry > 0, "retryAfter must be positive when denied")
}

func TestAllow_RefillsOverTime(t *testing.T) {
	now := time.Now()
	l := New(2, 2, WithClock(func() time.Time { return now }))
	// Drain.
	require.True(t, mustAllow(t, l, "k"))
	require.True(t, mustAllow(t, l, "k"))
	require.False(t, mustAllow(t, l, "k"))

	// Advance 1 second → 2 tokens refill.
	now = now.Add(time.Second)
	require.True(t, mustAllow(t, l, "k"))
	require.True(t, mustAllow(t, l, "k"))
	require.False(t, mustAllow(t, l, "k"))
}

func TestAllow_PerKeyIsolation(t *testing.T) {
	l := New(1, 1)
	require.True(t, mustAllow(t, l, "alice"))
	// alice is drained but bob's bucket is independent.
	require.True(t, mustAllow(t, l, "bob"))
	require.False(t, mustAllow(t, l, "alice"))
	require.False(t, mustAllow(t, l, "bob"))
}

func TestNew_PanicsOnInvalidParams(t *testing.T) {
	cases := []struct {
		name     string
		capacity float64
		refill   float64
	}{
		{name: "zero capacity", capacity: 0, refill: 1},
		{name: "negative capacity", capacity: -1, refill: 1},
		{name: "nan capacity", capacity: math.NaN(), refill: 1},
		{name: "infinite capacity", capacity: math.Inf(1), refill: 1},
		{name: "zero refill", capacity: 1, refill: 0},
		{name: "nan refill", capacity: 1, refill: math.NaN()},
		{name: "infinite refill", capacity: 1, refill: math.Inf(1)},
		{name: "unrepresentably slow refill", capacity: 1, refill: math.SmallestNonzeroFloat64},
		{name: "boundary slow refill", capacity: 1, refill: minRepresentableRefillPerSec},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Panics(t, func() { New(tc.capacity, tc.refill) })
		})
	}
}

func TestNew_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() { New(1, 1, nil) })
}

func TestInvalidReceiverReturnsError(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		l    *Limiter
	}{
		{"nil", nil},
		{"zero", &Limiter{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, retry, err := tc.l.Allow(ctx, "k")
			assert.False(t, ok)
			assert.Equal(t, time.Duration(0), retry)
			assert.ErrorIs(t, err, ratelimit.ErrInvalidLimiter)

			assert.NotPanics(t, func() { _ = tc.l.Close() })
			assert.Equal(t, 0, tc.l.Len())
		})
	}
}

func TestRetryAfter_AccurateWhenDenied(t *testing.T) {
	now := time.Now()
	l := New(1, 1, WithClock(func() time.Time { return now })) // 1 token/sec
	require.True(t, mustAllow(t, l, "k"))

	ok, retry, err := l.Allow(context.Background(), "k")
	require.NoError(t, err)
	require.False(t, ok)
	// We just spent the only token; refill rate is 1/s → wait ≈ 1s.
	assert.InDelta(t, time.Second, retry, float64(50*time.Millisecond))
}

// TestRefill_HighRateAllowsImmediateRetry pins the wave 125 semantics:
// the underlying [golang.org/x/time/rate.Limiter] treats math.MaxFloat64
// as rate.Inf, so a bucket draining at that refill rate is effectively
// always allowed. The previous arithmetic returned a 1ns wait at this
// edge; the new wrapper short-circuits the deny path because there is
// no meaningful wait to surface.
func TestRefill_HighRateAllowsImmediateRetry(t *testing.T) {
	now := time.Now()
	l := New(1, math.MaxFloat64, WithClock(func() time.Time { return now }))
	require.True(t, mustAllow(t, l, "k"))

	ok, retry, err := l.Allow(context.Background(), "k")
	require.NoError(t, err)
	assert.True(t, ok, "math.MaxFloat64 refill collapses to rate.Inf and must always allow")
	assert.Zero(t, retry)
}

// TestNew_FractionalCapacityRejectsAllRequests pins the wave 125
// behaviour for fractional capacities in (0, 1): rate.NewLimiter takes
// an integer burst, so int(0.5) == 0 produces a bucket that can never
// satisfy a 1-token reservation. Allow surfaces
// [ratelimit.ErrInvalidLimiter] so callers learn at first use.
func TestNew_FractionalCapacityRejectsAllRequests(t *testing.T) {
	l := New(0.5, 1)
	t.Cleanup(func() { _ = l.Close() })

	ok, retry, err := l.Allow(context.Background(), "k")
	assert.False(t, ok)
	assert.Zero(t, retry)
	assert.ErrorIs(t, err, ratelimit.ErrInvalidLimiter)
}

func mustAllow(t *testing.T, l *Limiter, key string) bool {
	t.Helper()
	ok, _, err := l.Allow(context.Background(), key)
	require.NoError(t, err)
	return ok
}

func TestWithSweeper_PanicsOnNonPositive(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		t.Run(d.String(), func(t *testing.T) {
			assert.Panics(t, func() {
				WithSweeper(d)
			})
		})
	}
}

// TestSweeper_RemovesColdBuckets bounds memory growth: a bucket that
// has fully refilled is indistinguishable from a fresh one and the
// sweeper reclaims it.
func TestSweeper_RemovesColdBuckets(t *testing.T) {
	var cur atomic.Int64
	cur.Store(time.Now().UnixNano())
	l := New(2, 2,
		WithClock(func() time.Time { return time.Unix(0, cur.Load()) }),
		WithSweeper(10*time.Millisecond),
	)
	t.Cleanup(func() { _ = l.Close() })

	for _, k := range []string{"a", "b", "c"} {
		ok, _, err := l.Allow(context.Background(), k)
		require.NoError(t, err)
		require.True(t, ok)
	}
	require.Equal(t, 3, l.Len())

	cur.Add(int64(10 * time.Second))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.Len() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, 0, l.Len(), "sweeper must drop fully-refilled buckets")
}

func TestClose_Idempotent(t *testing.T) {
	l := New(1, 1, WithSweeper(time.Hour))
	require.NoError(t, l.Close())
	require.NoError(t, l.Close())
}

func TestWithClock_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithClock(nil) })
}

// TestAllow_HonorsCancelledContext pins H-011: a cancelled ctx must
// return ctx.Err() without spending a token, so memory and Redis
// wirings agree about what a cancelled caller observes.
func TestAllow_HonorsCancelledContext(t *testing.T) {
	l := New(1, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ok, retry, err := l.Allow(ctx, "k")
	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, ok)
	assert.Equal(t, time.Duration(0), retry)

	ok, _, err = l.Allow(context.Background(), "k")
	require.NoError(t, err)
	require.True(t, ok)
}
