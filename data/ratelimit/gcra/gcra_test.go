package gcra

import (
	"context"
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
	l := New(time.Second, 5)
	allowed, _, err := l.Allow(context.Background(), "")
	assert.False(t, allowed)
	assert.ErrorIs(t, err, ratelimit.ErrInvalidKey)
}

func TestAllow_RejectsInvalidKey(t *testing.T) {
	l := New(time.Second, 5)

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

func TestAllow_AdmitsBurstAtStart(t *testing.T) {
	now := time.Now()
	l := New(time.Second, 5, WithClock(func() time.Time { return now }))
	// 5 in a row at the same instant — within burst tolerance.
	for i := 0; i < 5; i++ {
		ok, _, err := l.Allow(context.Background(), "k")
		require.NoError(t, err)
		assert.True(t, ok, "burst slot %d should admit", i+1)
	}
	// 6th rejected.
	ok, retry, err := l.Allow(context.Background(), "k")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.True(t, retry > 0)
}

func TestAllow_SmoothRateAfterBurst(t *testing.T) {
	now := time.Now()
	l := New(time.Second, 5, WithClock(func() time.Time { return now })) // 5/s smoothed; rate=200ms
	// Drain the burst.
	for i := 0; i < 5; i++ {
		require.True(t, mustAllow(t, l, "k"))
	}
	// Burst exhausted: another admit at the same instant must deny.
	ok, _, err := l.Allow(context.Background(), "k")
	require.NoError(t, err)
	assert.False(t, ok, "post-burst same-instant must deny")

	// Wait one rate (200ms) — the next slot opens.
	now = now.Add(200 * time.Millisecond)
	require.True(t, mustAllow(t, l, "k"), "admit after one rate-period of waiting")

	// Same instant again — denied.
	ok, _, err = l.Allow(context.Background(), "k")
	require.NoError(t, err)
	assert.False(t, ok, "second admit at the same instant after smoothed admit must deny")
}

func TestAllow_PerKeyIsolation(t *testing.T) {
	now := time.Now()
	l := New(time.Second, 1, WithClock(func() time.Time { return now }))
	require.True(t, mustAllow(t, l, "alice"))
	require.True(t, mustAllow(t, l, "bob"))
	// Same-instant repeat per key must deny (burst=1 → one admit then wait).
	require.False(t, mustAllow(t, l, "alice"))
	require.False(t, mustAllow(t, l, "bob"))
}

func TestNew_PanicsOnInvalidParams(t *testing.T) {
	assert.Panics(t, func() { New(0, 1) })
	assert.Panics(t, func() { New(time.Second, 0) })
	assert.Panics(t, func() { New(-time.Second, 1) })
}

func TestNew_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() { New(time.Second, 1, nil) })
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

func TestRetryAfter_PositiveWhenDenied(t *testing.T) {
	now := time.Now()
	l := New(time.Second, 1, WithClock(func() time.Time { return now }))
	require.True(t, mustAllow(t, l, "k"))

	ok, retry, err := l.Allow(context.Background(), "k")
	require.NoError(t, err)
	require.False(t, ok)
	assert.True(t, retry > 0)
}

func mustAllow(t *testing.T, l *Limiter, key string) bool {
	t.Helper()
	ok, _, err := l.Allow(context.Background(), key)
	require.NoError(t, err)
	return ok
}

// TestNew_PanicsWhenRateRoundsToZero pins the degenerate config:
// burst exceeding the period in nanoseconds collapses the emission
// interval to zero and would admit every event without spacing.
func TestNew_PanicsWhenRateRoundsToZero(t *testing.T) {
	assert.Panics(t, func() { New(time.Nanosecond, 2) },
		"period (1ns) / burst (2) rounds to 0 — must panic")
	assert.Panics(t, func() { New(10*time.Nanosecond, 100) },
		"period (10ns) / burst (100) rounds to 0 — must panic")
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

// TestSweeper_RemovesColdKeys exercises the bounded-cardinality
// guarantee. Keys whose theoretical arrival time has elapsed must be
// reclaimed by the sweeper.
func TestSweeper_RemovesColdKeys(t *testing.T) {
	var cur atomic.Int64
	cur.Store(time.Now().UnixNano())
	l := New(time.Second, 1,
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

	cur.Add(int64(2 * time.Second))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if l.Len() == 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	assert.Equal(t, 0, l.Len(), "sweeper must drop cold keys whose TAT has elapsed")
}

func TestClose_Idempotent(t *testing.T) {
	l := New(time.Second, 1, WithSweeper(time.Hour))
	require.NoError(t, l.Close())
	require.NoError(t, l.Close())
}

func TestAllow_RetryAtAdvertisedBoundaryAdmits(t *testing.T) {
	cur := time.Now()
	l := New(time.Second, 1, WithClock(func() time.Time { return cur }))
	require.True(t, mustAllow(t, l, "k"))

	ok, retry, err := l.Allow(context.Background(), "k")
	require.NoError(t, err)
	require.False(t, ok)
	require.Greater(t, retry, time.Nanosecond)

	cur = cur.Add(retry - time.Nanosecond)
	ok, _, err = l.Allow(context.Background(), "k")
	require.NoError(t, err)
	assert.True(t, ok, "retry landing exactly on allowAt must admit, not deny again")
}

func TestWithClock_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() { WithClock(nil) })
}
