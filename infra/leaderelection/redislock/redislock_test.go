package redislock

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
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

// hangingLockHandle simulates a Redis client without a read timeout: its
// Extend blocks until the *passed-in* context is cancelled. The only way
// the renew call can return is if the elector bounds it with a per-call
// deadline — an un-bounded leader ctx leaves Extend hung, which pins the
// elector loop and leaves OnAcquired's ctx un-cancelled while another
// replica becomes leader (overlap past one renewal interval).
type hangingLockHandle struct {
	released atomic.Bool
	extendCt atomic.Int32
}

func (h *hangingLockHandle) Release(_ context.Context) error { h.released.Store(true); return nil }
func (h *hangingLockHandle) Extend(ctx context.Context) (bool, error) {
	h.extendCt.Add(1)
	<-ctx.Done()
	return false, ctx.Err()
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

// TestHoldLeadership_HungExtendDoesNotPinElector pins the renew-call
// timeout: a hung Extend (Redis client without a read timeout) must not
// block the elector loop indefinitely. The renew call must be bounded by
// a per-call deadline so a stuck Extend is treated as a renewal failure,
// the leader ctx is cancelled, OnAcquired drains, and holdLeadership
// returns within roughly one renewal interval — keeping the overlap
// window bounded as the package doc promises.
func TestHoldLeadership_HungExtendDoesNotPinElector(t *testing.T) {
	e := &Elector{
		renewInterval: 20 * time.Millisecond,
		drainWarnTick: time.Hour, // suppress drain warnings
	}
	handle := &hangingLockHandle{}

	var cancelled atomic.Bool
	cb := leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) {
			<-ctx.Done()
			cancelled.Store(true)
		},
	}

	// Parent ctx is generous: holdLeadership must return on its own via
	// the bounded renew call, NOT because the parent deadline fired.
	parent, parentCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer parentCancel()

	result := make(chan error, 1)
	start := time.Now()
	go func() {
		result <- e.holdLeadership(parent, handle, cb)
	}()

	select {
	case err := <-result:
		require.Error(t, err, "hung Extend must surface a renewal failure")
		require.ErrorContains(t, err, "extend")
		require.True(t, cancelled.Load(), "leader ctx must be cancelled when Extend hangs")
		// Must return well within the parent deadline; a few renew
		// intervals of slack covers scheduling jitter.
		require.Less(t, time.Since(start), time.Second,
			"holdLeadership pinned by hung Extend — renew call is not bounded")
	case <-time.After(2 * time.Second):
		t.Fatal("holdLeadership pinned by hung Extend — renew call has no per-call deadline")
	}

	require.GreaterOrEqual(t, handle.extendCt.Load(), int32(1), "Extend must have been attempted")
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

func TestHoldLeadership_LossDoesNotReturnUntilCallbackDrains(t *testing.T) {
	e := &Elector{
		renewInterval: 10 * time.Millisecond,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(false)
	stub := &stubAcquirer{handle: handle}

	started := make(chan struct{})
	cancelled := make(chan struct{})
	released := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(released) })
	})

	result := make(chan error, 1)
	go func() {
		result <- runWithStub(t, e, stub, leaderelection.Callbacks{
			OnAcquired: func(ctx context.Context) {
				close(started)
				<-ctx.Done()
				close(cancelled)
				<-released
			},
		})
	}()

	select {
	case <-started:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnAcquired did not start")
	}
	select {
	case <-cancelled:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("OnAcquired was not cancelled after lock loss")
	}
	select {
	case err := <-result:
		t.Fatalf("holdLeadership returned before callback drained: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(released) })
	select {
	case err := <-result:
		require.ErrorContains(t, err, "handle reports lost")
	case <-time.After(100 * time.Millisecond):
		t.Fatal("holdLeadership did not return after callback drained")
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
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	NewWithLocker(rlock.NewLocker(nil), "key", nil)
}

func TestHoldLeadership_LongCallbackEmitsWarnAndMetric(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))

	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	e := &Elector{
		key:           "tenant-sweeper",
		renewInterval: 5 * time.Millisecond,
		drainWarnTick: 10 * time.Millisecond,
		logger:        logger,
		metrics:       metrics,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(false) // force loss path so cancel + awaitCallbackDrain runs

	released := make(chan struct{})
	cb := leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) {
			<-ctx.Done()
			// Hold past at least two warn ticks so the test asserts
			// repeated warn emission, not just one-shot wiring.
			<-released
		},
	}

	result := make(chan error, 1)
	go func() {
		result <- e.holdLeadership(context.Background(), handle, cb)
	}()

	// Wait long enough for multiple warn ticks to fire on a stuck callback.
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(metrics.drainWarns.WithLabelValues("tenant-sweeper")) >= 2
	}, time.Second, 5*time.Millisecond, "expected drain warn metric to increment at least twice")

	require.Contains(t, logBuf.String(), "OnAcquired callback still draining")
	// `key` is logged via redact.String — verify it shows up as a
	// redacted attribute rather than asserting on the raw key string.
	require.Contains(t, logBuf.String(), "key=")

	close(released)

	select {
	case err := <-result:
		require.ErrorContains(t, err, "handle reports lost")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("holdLeadership did not return after callback drained")
	}

	// Terminal drained observation must also be recorded so the
	// drained-state histogram is non-empty for SLO dashboards.
	drainedHistogramHasObservation := func() bool {
		// Collect metrics and inspect the drained-state vec for our key.
		ch := make(chan prometheus.Metric, 16)
		metrics.drainDuration.Collect(ch)
		close(ch)
		for m := range ch {
			out := &dto.Metric{}
			if err := m.Write(out); err != nil {
				continue
			}
			var key, state string
			for _, lp := range out.Label {
				switch lp.GetName() {
				case "key":
					key = lp.GetValue()
				case "state":
					state = lp.GetValue()
				}
			}
			if key == "tenant-sweeper" && state == drainStateDrained && out.Histogram != nil && out.Histogram.GetSampleCount() > 0 {
				return true
			}
		}
		return false
	}
	require.True(t, drainedHistogramHasObservation(), "drained histogram must record terminal observation")

	// Sanity: nothing crashed and the warn log mentions the key.
	require.True(t, strings.Contains(logBuf.String(), "elapsed"))
}

// TestHoldLeadership_DrainTimeoutAbandonsStalledCallback pins H-008:
// a callback that ignores ctx no longer pins the redislock elector
// when WithCallbackDrainTimeout is configured.
func TestHoldLeadership_DrainTimeoutAbandonsStalledCallback(t *testing.T) {
	e := &Elector{
		renewInterval: 10 * time.Millisecond,
		drainWarnTick: time.Hour,
		drainTimeout:  30 * time.Millisecond,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(false)

	started := make(chan struct{})
	err := e.holdLeadership(context.Background(), handle, leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) {
			close(started)
			select {}
		},
	})
	require.ErrorIs(t, err, ErrCallbackDrainTimeout)
	require.ErrorContains(t, err, "handle reports lost")

	select {
	case <-started:
	default:
		t.Fatal("OnAcquired did not start — test sanity check failed")
	}
}

// TestHoldLeadership_DrainTimeoutNotTrippedWhenCallbackCooperates pins
// the happy path for redislock's WithCallbackDrainTimeout.
func TestHoldLeadership_DrainTimeoutNotTrippedWhenCallbackCooperates(t *testing.T) {
	e := &Elector{
		renewInterval: 10 * time.Millisecond,
		drainWarnTick: time.Hour,
		drainTimeout:  500 * time.Millisecond,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(false)

	err := e.holdLeadership(context.Background(), handle, leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) {
			<-ctx.Done()
		},
	})
	require.ErrorContains(t, err, "handle reports lost")
	require.NotErrorIs(t, err, ErrCallbackDrainTimeout)
}
