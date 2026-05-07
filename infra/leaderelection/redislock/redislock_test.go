package redislock

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	rlock "github.com/bds421/rho-kit/data/lock/redislock"
	"github.com/bds421/rho-kit/infra/leaderelection"
)

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
