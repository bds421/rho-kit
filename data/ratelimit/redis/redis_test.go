package redis

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/ratelimit"
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
