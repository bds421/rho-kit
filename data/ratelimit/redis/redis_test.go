package redis

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/v2/ratelimit"
)

func newTestClient(t *testing.T) (goredis.UniversalClient, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return client, mr
}

func TestAllow_RejectsEmptyKey(t *testing.T) {
	client, _ := newTestClient(t)
	l := New(client, time.Second, 5)
	ok, _, err := l.Allow(context.Background(), "")
	assert.False(t, ok)
	assert.ErrorIs(t, err, ratelimit.ErrInvalidKey)
}

func TestAllow_RejectsInvalidKey(t *testing.T) {
	client, _ := newTestClient(t)
	l := New(client, time.Second, 5)

	cases := []string{
		"tenant\nid",
		"tenant\rid",
		"tenant\x00id",
		string([]byte{'t', 'e', 'n', 0xff}),
		strings.Repeat("a", ratelimit.MaxKeyLen+1),
	}
	for _, key := range cases {
		ok, retry, err := l.Allow(context.Background(), key)
		assert.False(t, ok)
		assert.Zero(t, retry)
		assert.ErrorIs(t, err, ratelimit.ErrInvalidKey)
		if len(key) > ratelimit.MaxKeyLen {
			assert.NotContains(t, err.Error(), "256")
			assert.NotContains(t, err.Error(), "257")
		}
	}
}

func TestAllow_AdmitsBurstAtSameInstant(t *testing.T) {
	client, _ := newTestClient(t)
	now := time.Unix(1_700_000_000, 0)
	l := New(client, time.Second, 5, WithClock(func() time.Time { return now }))

	for i := 0; i < 5; i++ {
		ok, _, err := l.Allow(context.Background(), "alice")
		require.NoError(t, err, "i=%d", i)
		assert.True(t, ok, "burst slot %d should admit at the same instant", i)
	}
	// Sixth at the same instant must deny.
	ok, retry, err := l.Allow(context.Background(), "alice")
	require.NoError(t, err)
	assert.False(t, ok, "sixth call at same instant must deny")
	assert.Greater(t, retry, time.Duration(0), "retryAfter must be positive when denied")
}

func TestAllow_AdmitsAfterDrip(t *testing.T) {
	client, _ := newTestClient(t)
	cur := time.Unix(1_700_000_000, 0)
	l := New(client, time.Second, 1, WithClock(func() time.Time { return cur }))

	// First admits.
	ok, _, err := l.Allow(context.Background(), "alice")
	require.NoError(t, err)
	require.True(t, ok)
	// Same instant denies (burst=1 already consumed).
	ok, _, _ = l.Allow(context.Background(), "alice")
	require.False(t, ok)
	// Advance one full rate (= period since burst=1).
	cur = cur.Add(time.Second)
	ok, _, _ = l.Allow(context.Background(), "alice")
	assert.True(t, ok, "after one period the next event must admit")
}

func TestAllow_SubMicrosecondRateRoundsUpConservatively(t *testing.T) {
	client, _ := newTestClient(t)
	cur := time.Unix(1_700_000_000, 0)
	l := New(client, time.Nanosecond, 1, WithClock(func() time.Time { return cur }))

	ok, _, err := l.Allow(context.Background(), "alice")
	require.NoError(t, err)
	require.True(t, ok)

	cur = cur.Add(time.Nanosecond)
	ok, retry, err := l.Allow(context.Background(), "alice")
	require.NoError(t, err)
	require.False(t, ok, "sub-microsecond rates are rounded up instead of collapsing at modern Unix timestamps")
	require.GreaterOrEqual(t, retry, time.Microsecond)

	cur = cur.Add(retry)
	ok, _, err = l.Allow(context.Background(), "alice")
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestAllow_KeysIsolated(t *testing.T) {
	client, _ := newTestClient(t)
	now := time.Unix(1_700_000_000, 0)
	l := New(client, time.Second, 1, WithClock(func() time.Time { return now }))

	okA, _, _ := l.Allow(context.Background(), "alice")
	okB, _, _ := l.Allow(context.Background(), "bob")
	assert.True(t, okA)
	assert.True(t, okB, "two different keys at the same instant must each admit once")
}

func TestAllow_AcrossClientsSharesState(t *testing.T) {
	// The whole point of the redis backend: limit applies across
	// app replicas. Two clients pointing at the same Redis must share
	// the budget.
	mr := miniredis.RunT(t)
	clientA := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	clientB := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = clientA.Close(); _ = clientB.Close() })

	now := time.Unix(1_700_000_000, 0)
	clk := WithClock(func() time.Time { return now })
	la := New(clientA, time.Second, 1, clk)
	lb := New(clientB, time.Second, 1, clk)

	okA, _, _ := la.Allow(context.Background(), "shared")
	okB, _, _ := lb.Allow(context.Background(), "shared")
	assert.True(t, okA)
	assert.False(t, okB, "second instance at same instant must observe the first's TAT and deny")
}

func TestNew_PanicsOnNilClient(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil client")
		}
	}()
	New(nil, time.Second, 1)
}

func TestNew_PanicsOnZeroPeriod(t *testing.T) {
	client, _ := newTestClient(t)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on zero period")
		}
	}()
	New(client, 0, 1)
}

func TestNew_PanicsOnZeroBurst(t *testing.T) {
	client, _ := newTestClient(t)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on zero burst")
		}
	}()
	New(client, time.Second, 0)
}

func TestNew_PanicsWhenRateRoundsToZero(t *testing.T) {
	client, _ := newTestClient(t)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when period/burst rounds to 0")
		}
	}()
	New(client, time.Nanosecond, 2)
}

func TestNew_PanicsOnTTLLessThanPeriod(t *testing.T) {
	client, _ := newTestClient(t)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on TTL < period")
		}
	}()
	New(client, 5*time.Minute, 1, WithKeyTTL(time.Second))
}

func TestCeilDurationSeconds_DoesNotOverflow(t *testing.T) {
	assert.Equal(t, int64(1), ceilDurationSeconds(time.Nanosecond))
	assert.Equal(t, int64(2), ceilDurationSeconds(time.Second+time.Nanosecond))
	assert.Equal(t, int64(maxDurationValue/time.Second)+1, ceilDurationSeconds(maxDurationValue))
}

func TestNew_PanicsOnNilOption(t *testing.T) {
	client, _ := newTestClient(t)
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	New(client, time.Second, 1, nil)
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
		})
	}
}

func TestWithKeyTTL_PanicsOnNonPositive(t *testing.T) {
	assert.Panics(t, func() { WithKeyTTL(0) })
	assert.Panics(t, func() { WithKeyTTL(-time.Second) })
}

func TestWithKeyPrefix_PanicsOnInvalid(t *testing.T) {
	cases := []string{
		"",
		"tenant\n",
		"tenant\r",
		"tenant\x00",
		"tenant key:",
		"tenant\tkey:",
		string([]byte{'p', 0xff}),
		strings.Repeat("a", maxKeyPrefixLen+1),
	}
	for _, prefix := range cases {
		assert.Panics(t, func() { WithKeyPrefix(prefix) })
	}
	assert.PanicsWithValue(t,
		"ratelimit/redis: WithKeyPrefix prefix exceeds maximum length",
		func() { WithKeyPrefix(strings.Repeat("a", maxKeyPrefixLen+1)) },
	)
}

func TestAllow_RetryAtAdvertisedBoundaryAdmits(t *testing.T) {
	client, _ := newTestClient(t)
	cur := time.Unix(1_700_000_000, 0)
	l := New(client, time.Second, 1, WithClock(func() time.Time { return cur }))

	ok, _, err := l.Allow(context.Background(), "alice")
	require.NoError(t, err)
	require.True(t, ok)

	ok, retry, err := l.Allow(context.Background(), "alice")
	require.NoError(t, err)
	require.False(t, ok)
	require.Greater(t, retry, time.Nanosecond)

	cur = cur.Add(retry - time.Nanosecond)
	ok, _, err = l.Allow(context.Background(), "alice")
	require.NoError(t, err)
	assert.True(t, ok, "retry landing exactly on allowAt must admit, not deny again")
}

func TestWithClock_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil clock")
		}
	}()
	WithClock(nil)
}

func TestParseScriptResult(t *testing.T) {
	cases := []struct {
		name        string
		res         any
		wantAllowed bool
		wantRetryUS int64
		wantErr     bool
	}{
		{"allowed", []any{int64(1), int64(0)}, true, 0, false},
		{"denied with retry", []any{int64(0), int64(5000)}, false, 5000, false},
		{"non-array", int64(1), false, 0, true},
		{"wrong length", []any{int64(1)}, false, 0, true},
		{"too long", []any{int64(1), int64(0), int64(0)}, false, 0, true},
		// Non-int64 members must surface an explicit error, NOT a silent
		// deny indistinguishable from a real rate-limit rejection.
		{"non-int allowed member", []any{"1", int64(0)}, false, 0, true},
		{"non-int retry member", []any{int64(0), "5000"}, false, 0, true},
		{"both members non-int", []any{1.0, 2.0}, false, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			allowed, retryUS, err := parseScriptResult(tc.res)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "unexpected script result shape")
				assert.False(t, allowed)
				assert.Zero(t, retryUS)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantAllowed, allowed)
			assert.Equal(t, tc.wantRetryUS, retryUS)
		})
	}
}

func TestAllow_ConcurrentSameKeyConvergesToBurst(t *testing.T) {
	// With burst=N, exactly N concurrent admits should happen at the
	// same instant; the rest must deny.
	client, _ := newTestClient(t)
	burst := 10
	now := time.Unix(1_700_000_000, 0)
	l := New(client, time.Second, burst, WithClock(func() time.Time { return now }))

	const callers = 50
	var ok atomic.Int64
	var deny atomic.Int64
	done := make(chan struct{})
	for i := 0; i < callers; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			admitted, _, err := l.Allow(context.Background(), "shared")
			if err != nil {
				return
			}
			if admitted {
				ok.Add(1)
			} else {
				deny.Add(1)
			}
		}()
	}
	for i := 0; i < callers; i++ {
		<-done
	}
	assert.Equal(t, int64(burst), ok.Load(), "exactly burst events must admit at the same instant")
	assert.Equal(t, int64(callers-burst), deny.Load(), "the rest must deny")
}
