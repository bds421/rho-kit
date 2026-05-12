package redislock

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	rlock "github.com/bds421/rho-kit/data/lock/redislock/v2"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
)

type releaseContextKey struct{}

// fakeLockHandle is the minimal lock.Lock used for tests. We can't drive
// rlock.Locker without a Redis instance — these tests cover the elector
// state machine via a stub locker swap.
type fakeLockHandle struct {
	released atomic.Bool
	extendOK atomic.Bool
	extendCt atomic.Int32
}

func (f *fakeLockHandle) Release(_ context.Context) error { f.released.Store(true); return nil }
func (f *fakeLockHandle) Extend(_ context.Context) (bool, error) {
	f.extendCt.Add(1)
	return f.extendOK.Load(), nil
}

func TestOptions_PanicOnInvalidDurations(t *testing.T) {
	for name, fn := range map[string]func(){
		"WithRetryInterval zero":     func() { WithRetryInterval(0) },
		"WithRetryInterval negative": func() { WithRetryInterval(-time.Second) },
		"WithRenewInterval zero":     func() { WithRenewInterval(0) },
		"WithRenewInterval negative": func() { WithRenewInterval(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			require.Panics(t, fn)
		})
	}
}

// stubAcquirer lets us simulate "always leader" or "lose after N
// renewals" without standing up Redis.
type stubAcquirer struct {
	handle *fakeLockHandle
}

func runWithStub(t *testing.T, e *Elector, stub *stubAcquirer, cb leaderelection.Callbacks) error {
	t.Helper()
	// Reach into the elector and replace the locker call by overriding
	// the elector's Run via a test-only path. We simulate by driving
	// holdLeadership directly.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	leaderCtx, leaderCancel := context.WithCancel(ctx)
	defer leaderCancel()
	return e.holdLeadership(leaderCtx, stub.handle, cb)
}

func TestHoldLeadership_CallbackCompletesNormally(t *testing.T) {
	e := &Elector{
		renewInterval: 10 * time.Millisecond,
		logger:        nil,
	}
	// nil logger is fine since holdLeadership doesn't log success.
	handle := &fakeLockHandle{}
	handle.extendOK.Store(true)
	stub := &stubAcquirer{handle: handle}

	called := atomic.Bool{}
	cb := leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) {
			called.Store(true)
			// Return immediately; holdLeadership should observe cbDone.
		},
	}
	err := runWithStub(t, e, stub, cb)
	require.NoError(t, err)
	require.True(t, called.Load())
}

func TestHoldLeadership_RenewalFailureExits(t *testing.T) {
	e := &Elector{
		renewInterval: 10 * time.Millisecond,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(false) // simulate lost lock
	stub := &stubAcquirer{handle: handle}

	cb := leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) {
			// Block on ctx so the renewal failure is the exit condition.
			<-ctx.Done()
		},
	}
	err := runWithStub(t, e, stub, cb)
	require.Error(t, err) // "handle reports lost"
}

func TestHoldLeadership_OnAcquiredPanicReturnsError(t *testing.T) {
	e := &Elector{
		renewInterval: 10 * time.Millisecond,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(true)
	stub := &stubAcquirer{handle: handle}

	err := runWithStub(t, e, stub, leaderelection.Callbacks{
		OnAcquired: func(context.Context) {
			panic("leader work exploded")
		},
	})
	require.ErrorContains(t, err, "OnAcquired panic")
	require.ErrorContains(t, err, "<redacted panic value: string>")
	require.NotContains(t, err.Error(), "leader work exploded")
}

func TestRun_DoesNotCallOnLostWithoutLeadership(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	e := &Elector{logger: slog.Default(), key: "leader"}

	var called atomic.Bool
	err := e.Run(ctx, leaderelection.Callbacks{
		OnLost: func() {
			called.Store(true)
		},
	})
	require.ErrorIs(t, err, context.Canceled)
	require.False(t, called.Load())
}

func TestRunOnLost_PanicReturned(t *testing.T) {
	e := &Elector{logger: slog.Default(), key: "leader"}

	err := e.runOnLost(leaderelection.Callbacks{
		OnLost: func() {
			panic("lost cleanup exploded")
		},
	})
	require.ErrorContains(t, err, "OnLost panic")
	require.ErrorContains(t, err, "<redacted panic value: string>")
	require.NotContains(t, err.Error(), "lost cleanup exploded")
}

func TestRun_RejectsNilContext(t *testing.T) {
	e := &Elector{logger: slog.Default(), key: "leader"}
	var ctx context.Context
	err := e.Run(ctx, leaderelection.Callbacks{})
	require.Error(t, err)
	require.ErrorContains(t, err, "non-nil context")
}

func TestHoldLeadership_LossCancelsAndWaitsForCallback(t *testing.T) {
	e := &Elector{
		renewInterval: 10 * time.Millisecond,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(false)
	stub := &stubAcquirer{handle: handle}

	var callbackExited atomic.Bool
	err := runWithStub(t, e, stub, leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) {
			<-ctx.Done()
			time.Sleep(10 * time.Millisecond)
			callbackExited.Store(true)
		},
	})
	require.ErrorContains(t, err, "handle reports lost")
	require.True(t, callbackExited.Load(), "leader work must drain before retry")
}

func TestHoldLeadership_CallbackDrainTimeoutReturnsDetached(t *testing.T) {
	e := &Elector{
		renewInterval:        10 * time.Millisecond,
		callbackDrainTimeout: 20 * time.Millisecond,
		logger:               slog.Default(),
		key:                  "leader",
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(false) // force renewal failure → cancel + await
	stub := &stubAcquirer{handle: handle}

	released := make(chan struct{})
	err := runWithStub(t, e, stub, leaderelection.Callbacks{
		OnAcquired: func(_ context.Context) {
			// Intentionally ignore ctx to simulate buggy user code.
			<-released
		},
	})
	require.Error(t, err)
	// Free the orphaned goroutine so the test doesn't leak.
	close(released)
}

func TestWithCallbackDrainTimeout_PanicsOnNonPositive(t *testing.T) {
	for name, fn := range map[string]func(){
		"zero":     func() { WithCallbackDrainTimeout(0) },
		"negative": func() { WithCallbackDrainTimeout(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) { require.Panics(t, fn) })
	}
}

func TestLeaderReleaseContextPreservesValuesAfterCancellation(t *testing.T) {
	parent := context.WithValue(context.Background(), releaseContextKey{}, "trace-123")
	ctx, cancel := context.WithCancel(parent)
	cancel()

	releaseCtx, releaseCancel := leaderReleaseContext(ctx, time.Second)
	defer releaseCancel()

	require.Equal(t, "trace-123", releaseCtx.Value(releaseContextKey{}))
	require.NoError(t, releaseCtx.Err())
}

func TestNewWithLocker_PanicsOnNil(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil locker")
		}
	}()
	NewWithLocker(nil, "key")
}

func TestNewWithLocker_PanicsOnEmptyKey(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty key")
		}
	}()
	NewWithLocker(rlock.NewLocker(nil), "")
}

func TestNewWithLocker_PanicsOnNilOption(t *testing.T) {
	require.Panics(t, func() {
		NewWithLocker(new(rlock.Locker), "key", nil)
	})
}
