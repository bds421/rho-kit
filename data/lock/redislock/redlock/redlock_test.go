package redlock_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredislib "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	redislock "github.com/bds421/rho-kit/data/lock/redislock/v2"
	"github.com/bds421/rho-kit/data/lock/redislock/v2/redlock"
	"github.com/bds421/rho-kit/data/v2/lock"
	"github.com/bds421/rho-kit/data/v2/lock/locktest"
)

// setupQuorum stands up n in-process miniredis instances and returns
// matched clients. Each test gets a fresh quorum to avoid cross-test
// leak through shared keyspace.
func setupQuorum(t *testing.T, n int) []goredislib.UniversalClient {
	t.Helper()
	clients := make([]goredislib.UniversalClient, 0, n)
	for i := 0; i < n; i++ {
		mr := miniredis.RunT(t)
		c := goredislib.NewClient(&goredislib.Options{
			Addr:               mr.Addr(),
			MaxRetries:         -1,
			DialerRetries:      1,
			DialerRetryTimeout: time.Millisecond,
		})
		t.Cleanup(func() { _ = c.Close() })
		clients = append(clients, c)
	}
	return clients
}

func TestNewQuorumLocker_PanicsOnUndersizedQuorum(t *testing.T) {
	assert.Panics(t, func() {
		redlock.NewQuorumLocker(nil)
	}, "nil clients slice must panic")
	assert.Panics(t, func() {
		redlock.NewQuorumLocker([]goredislib.UniversalClient{})
	}, "empty clients slice must panic")
	assert.Panics(t, func() {
		mr := miniredis.RunT(t)
		c := goredislib.NewClient(&goredislib.Options{Addr: mr.Addr()})
		redlock.NewQuorumLocker([]goredislib.UniversalClient{c, c})
	}, "two clients (even pointing at different instances) must panic — N=2 cannot tolerate any failure")
}

func TestNewQuorumLocker_PanicsOnNilClient(t *testing.T) {
	mr := miniredis.RunT(t)
	c := goredislib.NewClient(&goredislib.Options{Addr: mr.Addr()})
	assert.Panics(t, func() {
		redlock.NewQuorumLocker([]goredislib.UniversalClient{c, nil, c})
	})
}

func TestNewQuorumLocker_PanicsOnBadOptions(t *testing.T) {
	clients := setupQuorum(t, 3)
	assert.Panics(t, func() { redlock.WithTTL(0) })
	assert.Panics(t, func() { redlock.WithTTL(-1) })
	assert.Panics(t, func() { redlock.WithRetry(0, 1) })
	assert.Panics(t, func() { redlock.WithRetry(time.Millisecond, -1) })
	assert.Panics(t, func() { redlock.WithMaxWait(0) })
	assert.Panics(t, func() {
		redlock.NewQuorumLocker(clients, nil)
	})
}

func TestOptionSurfaceMatchesRedislock(t *testing.T) {
	// Both packages intentionally alias the same Option type. Passing each
	// package's options to the other constructor pins both directions so the
	// public surfaces cannot silently drift.
	clients := setupQuorum(t, 3)
	assert.NotNil(t, redlock.NewQuorumLocker(clients,
		redislock.WithTTL(time.Second),
		redislock.WithRetry(time.Millisecond, 1),
		redislock.WithMaxWait(time.Second),
		redislock.WithLogger(nil),
		redislock.WithKeyPrefix("shared:"),
	))
	assert.NotNil(t, redislock.NewLocker(clients[0],
		redlock.WithTTL(time.Second),
		redlock.WithRetry(time.Millisecond, 1),
		redlock.WithMaxWait(time.Second),
		redlock.WithLogger(nil),
		redlock.WithKeyPrefix("shared:"),
	))
}

func TestQuorumLocker_AcquireAndRelease(t *testing.T) {
	clients := setupQuorum(t, 3)
	q := redlock.NewQuorumLocker(clients, redlock.WithTTL(5*time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	l, ok, err := q.Acquire(ctx, "kit/redlock/test/basic")
	require.NoError(t, err)
	require.True(t, ok, "first acquire on an idle quorum must succeed")
	require.NotNil(t, l)

	require.NoError(t, l.Release(ctx))
}

func TestQuorumLocker_ContentionReturnsNotAcquired(t *testing.T) {
	clients := setupQuorum(t, 3)
	q := redlock.NewQuorumLocker(clients, redlock.WithTTL(5*time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	first, ok, err := q.Acquire(ctx, "kit/redlock/test/contention")
	require.NoError(t, err)
	require.True(t, ok)
	defer func() { _ = first.Release(ctx) }()

	// A second caller (different mutex token) must see contention.
	second, ok, err := q.Acquire(ctx, "kit/redlock/test/contention")
	require.NoError(t, err, "contention is a (nil, false, nil) outcome, not an error")
	assert.False(t, ok)
	assert.Nil(t, second)
}

// TestQuorumLocker_SurvivesMinorityFailure verifies the production-
// critical property of Redlock: a single instance loss must not
// prevent acquisition because a majority remains. With N=3 we kill
// one and expect acquire to still succeed.
func TestQuorumLocker_SurvivesMinorityFailure(t *testing.T) {
	// Stand up three instances directly so we can kill one mid-test
	// rather than going through the t.Cleanup-driven helper.
	mrs := make([]*miniredis.Miniredis, 3)
	clients := make([]goredislib.UniversalClient, 3)
	for i := 0; i < 3; i++ {
		mrs[i] = miniredis.RunT(t)
		clients[i] = goredislib.NewClient(&goredislib.Options{
			Addr:               mrs[i].Addr(),
			MaxRetries:         -1,
			DialerRetries:      1,
			DialerRetryTimeout: time.Millisecond,
		})
		t.Cleanup(func() { _ = clients[i].Close() })
	}

	q := redlock.NewQuorumLocker(clients,
		redlock.WithTTL(2*time.Second),
		redlock.WithRetry(20*time.Millisecond, 3),
	)

	// Take one instance offline before Acquire. Redsync should still
	// reach a 2-of-3 majority on the survivors.
	mrs[0].Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	l, ok, err := q.Acquire(ctx, "kit/redlock/test/minority-failure")
	require.NoError(t, err)
	assert.True(t, ok, "quorum acquire must succeed when only a minority is down")
	if l != nil {
		_ = l.Release(ctx)
	}
}

// TestQuorumLocker_LosesWithoutMajority verifies the dual property:
// when the majority is unavailable the algorithm REFUSES to grant the
// lock, returning (nil, false, nil) — graceful no-acquire rather than
// silently handing out an unsafe lock.
func TestQuorumLocker_LosesWithoutMajority(t *testing.T) {
	mrs := make([]*miniredis.Miniredis, 3)
	clients := make([]goredislib.UniversalClient, 3)
	for i := 0; i < 3; i++ {
		mrs[i] = miniredis.RunT(t)
		clients[i] = goredislib.NewClient(&goredislib.Options{
			Addr:               mrs[i].Addr(),
			MaxRetries:         -1,
			DialerRetries:      1,
			DialerRetryTimeout: time.Millisecond,
		})
		t.Cleanup(func() { _ = clients[i].Close() })
	}

	q := redlock.NewQuorumLocker(clients,
		redlock.WithTTL(1*time.Second),
		redlock.WithRetry(10*time.Millisecond, 2),
		redlock.WithMaxWait(200*time.Millisecond),
	)

	// Take TWO instances offline — quorum is now impossible.
	mrs[0].Close()
	mrs[1].Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, ok, _ := q.Acquire(ctx, "kit/redlock/test/no-majority")
	assert.False(t, ok, "quorum acquire MUST refuse when only a minority is reachable")
}

func TestQuorumLocker_WithLock_RunsBodyAndReleases(t *testing.T) {
	clients := setupQuorum(t, 3)
	q := redlock.NewQuorumLocker(clients)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	called := false
	err := q.WithLock(ctx, "kit/redlock/test/withlock", func(_ context.Context) error {
		called = true
		// A second Acquire mid-fn must see contention — proves the
		// lock is actually held during fn.
		_, ok, _ := q.Acquire(ctx, "kit/redlock/test/withlock")
		assert.False(t, ok, "lock must be held during fn")
		return nil
	})
	require.NoError(t, err)
	assert.True(t, called)

	// After WithLock returns, the lock must be re-acquirable.
	l, ok, err := q.Acquire(ctx, "kit/redlock/test/withlock")
	require.NoError(t, err)
	assert.True(t, ok, "WithLock must release on return so the key is re-acquirable")
	if l != nil {
		_ = l.Release(ctx)
	}
}

func TestQuorumLocker_WithLock_ReleasesOnPanic(t *testing.T) {
	clients := setupQuorum(t, 3)
	q := redlock.NewQuorumLocker(clients)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	assert.Panics(t, func() {
		_ = q.WithLock(ctx, "kit/redlock/test/panic", func(_ context.Context) error {
			panic("body misbehaved")
		})
	})

	// Critical invariant: even a panicking body must not orphan the
	// lock — the deferred release runs and the key is re-acquirable.
	l, ok, err := q.Acquire(ctx, "kit/redlock/test/panic")
	require.NoError(t, err)
	assert.True(t, ok, "panic in WithLock body must not orphan the lock")
	if l != nil {
		_ = l.Release(ctx)
	}
}

func TestQuorumLocker_Release_DoubleReleaseReportsLost(t *testing.T) {
	clients := setupQuorum(t, 3)
	q := redlock.NewQuorumLocker(clients)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	l, ok, err := q.Acquire(ctx, "kit/redlock/test/double-release")
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, l.Release(ctx), "first release succeeds")
	// A second Release on an already-released handle must report
	// lock.ErrLockLost — the same contract as redislock and
	// pgadvisory, so callers swapping backends through the
	// lock.Locker interface (and the locktest conformance suite)
	// see identical behaviour. Callers that want idempotent cleanup
	// detect it via errors.Is(err, lock.ErrLockLost).
	err = l.Release(ctx)
	require.Error(t, err, "Release on an already-released handle must return an error")
	assert.ErrorIs(t, err, lock.ErrLockLost, "the error MUST be lock.ErrLockLost so callers can errors.Is detect it")
}

func TestQuorumLocker_Extend(t *testing.T) {
	clients := setupQuorum(t, 3)
	q := redlock.NewQuorumLocker(clients, redlock.WithTTL(2*time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	l, ok, err := q.Acquire(ctx, "kit/redlock/test/extend")
	require.NoError(t, err)
	require.True(t, ok)
	defer func() { _ = l.Release(ctx) }()

	extended, err := l.Extend(ctx)
	require.NoError(t, err)
	assert.True(t, extended, "extend must succeed for the current holder")
}

func TestQuorumLocker_Extend_AfterRelease_ReportsLost(t *testing.T) {
	clients := setupQuorum(t, 3)
	q := redlock.NewQuorumLocker(clients)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	l, ok, err := q.Acquire(ctx, "kit/redlock/test/extend-after-release")
	require.NoError(t, err)
	require.True(t, ok)

	require.NoError(t, l.Release(ctx))

	// Extend after Release must report "no longer owned" via the
	// (false, nil) contract, not a backend error.
	extended, err := l.Extend(ctx)
	require.NoError(t, err)
	assert.False(t, extended, "Extend after Release must return (false, nil)")
}

func TestQuorumLocker_BadKey_Rejected(t *testing.T) {
	clients := setupQuorum(t, 3)
	q := redlock.NewQuorumLocker(clients)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	cases := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"control byte", "kit\x01key"},
		{"newline", "kit\nkey"},
		{"too long", strings.Repeat("x", redlock.MaxLockKeyLen+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ok, err := q.Acquire(ctx, tc.key)
			require.Error(t, err)
			assert.False(t, ok)
		})
	}
}

func TestQuorumLocker_SatisfiesLockerInterface(t *testing.T) {
	clients := setupQuorum(t, 3)
	var locker lock.Locker = redlock.NewQuorumLocker(clients)
	_ = locker
}

// TestQuorumLocker_Conformance runs the kit's lock.Locker conformance
// battery against the quorum locker so its behaviour is provably
// identical to redislock and pgadvisory (as locktest's package doc
// claims). Each subtest gets a fresh in-process quorum.
func TestQuorumLocker_Conformance(t *testing.T) {
	locktest.Run(t, func(t *testing.T) lock.Locker {
		// Configure retries so the quorum algorithm resolves the
		// suite's 16-way concurrent contention to a winner instead
		// of every contender giving up after a single try. This
		// mirrors how the redislock conformance runs against a real
		// broker that retries under contention.
		return redlock.NewQuorumLocker(setupQuorum(t, 3),
			redlock.WithRetry(5*time.Millisecond, 32),
		)
	})
}

// TestQuorumLocker_WithLock_PropagatesFnError ensures the body's
// error is returned cleanly when no lock-lost condition was hit.
func TestQuorumLocker_WithLock_PropagatesFnError(t *testing.T) {
	clients := setupQuorum(t, 3)
	q := redlock.NewQuorumLocker(clients)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	bodyErr := errors.New("application-specific failure")
	err := q.WithLock(ctx, "kit/redlock/test/fn-error", func(_ context.Context) error {
		return bodyErr
	})
	require.ErrorIs(t, err, bodyErr)
}

func TestLockerWithValue(t *testing.T) {
	clients := setupQuorum(t, 3)
	q := redlock.NewQuorumLocker(clients, redlock.WithTTL(5*time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	got, err := redlock.LockerWithValue(ctx, q, "kit/redlock/test/withvalue", func(_ context.Context) (int, error) {
		return 42, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 42, got)
}

func TestLockerWithValue_Contention(t *testing.T) {
	clients := setupQuorum(t, 3)
	q := redlock.NewQuorumLocker(clients, redlock.WithTTL(5*time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	first, ok, err := q.Acquire(ctx, "kit/redlock/test/withvalue-contention")
	require.NoError(t, err)
	require.True(t, ok)
	defer func() { _ = first.Release(ctx) }()

	_, err = redlock.LockerWithValue(ctx, q, "kit/redlock/test/withvalue-contention", func(context.Context) (int, error) {
		t.Fatal("contended LockerWithValue must not run callback")
		return 0, nil
	})
	require.ErrorIs(t, err, lock.ErrNotAcquired)
}
