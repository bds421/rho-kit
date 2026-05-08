package tokenbucket

import (
	"context"
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
	assert.Panics(t, func() { New(0, 1) })
	assert.Panics(t, func() { New(1, 0) })
	assert.Panics(t, func() { New(-1, 1) })
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

func mustAllow(t *testing.T, l *Limiter, key string) bool {
	t.Helper()
	ok, _, err := l.Allow(context.Background(), key)
	require.NoError(t, err)
	return ok
}

// TestSweeper_RemovesColdBuckets bounds memory growth: a bucket that
// has fully refilled is indistinguishable from a fresh one and the
// sweeper reclaims it.
func TestSweeper_RemovesColdBuckets(t *testing.T) {
	cur := time.Now()
	l := New(2, 2,
		WithClock(func() time.Time { return cur }),
		WithSweeper(10*time.Millisecond),
	)
	t.Cleanup(l.Stop)

	for _, k := range []string{"a", "b", "c"} {
		ok, _, err := l.Allow(context.Background(), k)
		require.NoError(t, err)
		require.True(t, ok)
	}
	require.Equal(t, 3, l.Len())

	cur = cur.Add(10 * time.Second)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.Len() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, 0, l.Len(), "sweeper must drop fully-refilled buckets")
}

func TestStop_Idempotent(t *testing.T) {
	l := New(1, 1, WithSweeper(time.Hour))
	l.Stop()
	l.Stop()
}

func TestWithClock_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithClock(nil) })
}
