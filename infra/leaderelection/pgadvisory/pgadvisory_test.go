package pgadvisory

import (
	"bytes"
	"context"
	"database/sql"
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

	"github.com/bds421/rho-kit/data/v2/lock"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
)

type releaseContextKey struct{}

type fakeLockHandle struct {
	released atomic.Bool
	extendOK atomic.Bool
}

func (f *fakeLockHandle) Release(context.Context) error { f.released.Store(true); return nil }
func (f *fakeLockHandle) Extend(context.Context) (bool, error) {
	return f.extendOK.Load(), nil
}

func TestOptions_PanicOnInvalidDurations(t *testing.T) {
	for name, fn := range map[string]func(){
		"WithRetryInterval zero":     func() { WithRetryInterval(0) },
		"WithRetryInterval negative": func() { WithRetryInterval(-time.Second) },
		"WithHealthCheck zero":       func() { WithHealthCheck(0) },
		"WithHealthCheck negative":   func() { WithHealthCheck(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			require.Panics(t, fn)
		})
	}
}

func TestNew_PanicsOnEmptyKey(t *testing.T) {
	require.Panics(t, func() {
		New(nil, "")
	})
}

func TestNew_PanicsOnNilOption(t *testing.T) {
	require.Panics(t, func() {
		New(&sql.DB{}, "leader", nil)
	})
}

func TestHoldLeadership_OnAcquiredPanicReturnsError(t *testing.T) {
	e := &Elector{
		healthCheck: time.Hour,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(true)

	err := e.holdLeadership(context.Background(), handle, leaderelection.Callbacks{
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
		healthCheck: 10 * time.Millisecond,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(false)

	var callbackExited atomic.Bool
	err := e.holdLeadership(context.Background(), handle, leaderelection.Callbacks{
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
		healthCheck: 10 * time.Millisecond,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(false)

	started := make(chan struct{})
	cancelled := make(chan struct{})
	released := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(released) })
	})

	result := make(chan error, 1)
	go func() {
		result <- e.holdLeadership(context.Background(), handle, leaderelection.Callbacks{
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

// TestHoldLeadership_DrainTimeoutAbandonsStalledCallback pins H-008:
// with WithCallbackDrainTimeout configured, a callback that ignores
// ctx must not pin the elector forever. Run returns
// ErrCallbackDrainTimeout joined with the underlying loss reason so
// the orchestrator can log + restart.
func TestHoldLeadership_DrainTimeoutAbandonsStalledCallback(t *testing.T) {
	e := &Elector{
		healthCheck:   10 * time.Millisecond,
		drainWarnTick: time.Hour, // suppress noise; we test the timeout path
		drainTimeout:  30 * time.Millisecond,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(false)

	started := make(chan struct{})
	// Callback never returns — the bug the audit warns about.
	err := e.holdLeadership(context.Background(), handle, leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) {
			close(started)
			select {} // intentionally hangs forever
		},
	})
	require.ErrorIs(t, err, ErrCallbackDrainTimeout,
		"drain timeout must surface ErrCallbackDrainTimeout in the joined error")
	require.ErrorContains(t, err, "handle reports lost",
		"original loss reason must remain joined with the timeout sentinel")

	select {
	case <-started:
	default:
		t.Fatal("OnAcquired did not start — test sanity check failed")
	}
}

// TestHoldLeadership_DrainTimeoutNotTrippedWhenCallbackCooperates
// pins the happy path: a callback that honours ctx must drain within
// the timeout and Run returns the plain loss error without the
// timeout sentinel.
func TestHoldLeadership_DrainTimeoutNotTrippedWhenCallbackCooperates(t *testing.T) {
	e := &Elector{
		healthCheck:   10 * time.Millisecond,
		drainWarnTick: time.Hour,
		drainTimeout:  500 * time.Millisecond,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(false)

	err := e.holdLeadership(context.Background(), handle, leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) {
			<-ctx.Done() // cooperative
		},
	})
	require.ErrorContains(t, err, "handle reports lost")
	require.NotErrorIs(t, err, ErrCallbackDrainTimeout,
		"cooperative callback must not trip the drain-timeout sentinel")
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

func TestHoldLeadership_LongCallbackEmitsWarnAndMetric(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))

	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	e := &Elector{
		key:           "tenant-sweeper",
		healthCheck:   5 * time.Millisecond,
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
	require.True(t, strings.Contains(logBuf.String(), "elapsed"))
}

// fakeLocker is an acquirer that hands out a configured handle so the
// Run acquire/retry loop can be exercised without a live Postgres.
type fakeLocker struct {
	handle      lock.Lock
	acquireN    atomic.Int32
	grantOnce   bool
	grantedOnce atomic.Bool
}

func (f *fakeLocker) Acquire(context.Context, string) (lock.Lock, bool, error) {
	f.acquireN.Add(1)
	if f.grantOnce && !f.grantedOnce.CompareAndSwap(false, true) {
		// Second and later calls report the lock as held elsewhere so a
		// (mis)behaving Run that re-acquires keeps spinning rather than
		// re-entering leadership — the test asserts on acquireN instead.
		return nil, false, nil
	}
	return f.handle, true, nil
}

// slowExtendHandle blocks Extend until its context is cancelled, then
// reports the lock as lost. It models a silent network drop on the
// pinned session connection: ExecContext would hang until the per-probe
// deadline fires.
type slowExtendHandle struct {
	released atomic.Bool
}

func (s *slowExtendHandle) Release(context.Context) error { s.released.Store(true); return nil }
func (s *slowExtendHandle) Extend(ctx context.Context) (bool, error) {
	<-ctx.Done()
	return false, ctx.Err()
}

// TestRun_DrainTimeoutDoesNotReacquire pins L-141 in pgadvisory: once
// the OnAcquired callback drain times out, an orphan goroutine from the
// previous term is still running. Run MUST return ErrCallbackDrainTimeout
// rather than looping to re-acquire — re-acquiring would let the same
// process re-enter leadership while the orphan still holds resources
// (in-process double leader). Sibling redislock already guards this.
func TestRun_DrainTimeoutDoesNotReacquire(t *testing.T) {
	handle := &fakeLockHandle{}
	handle.extendOK.Store(false) // force loss so the drain path runs

	locker := &fakeLocker{handle: handle}

	var onAcquiredN atomic.Int32
	released := make(chan struct{})
	t.Cleanup(func() { close(released) })

	e := &Elector{
		locker:        locker,
		key:           "leader",
		retryInterval: time.Millisecond,
		healthCheck:   5 * time.Millisecond,
		drainWarnTick: time.Hour,
		drainTimeout:  20 * time.Millisecond,
		logger:        slog.Default(),
	}

	result := make(chan error, 1)
	go func() {
		result <- e.Run(context.Background(), leaderelection.Callbacks{
			OnAcquired: func(context.Context) {
				onAcquiredN.Add(1)
				<-released // never returns before the drain deadline
			},
		})
	}()

	select {
	case err := <-result:
		require.ErrorIs(t, err, ErrCallbackDrainTimeout,
			"Run must surface ErrCallbackDrainTimeout instead of re-acquiring")
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after drain timeout — it likely looped to re-acquire")
	}

	require.Equal(t, int32(1), onAcquiredN.Load(),
		"OnAcquired must run exactly once; a re-acquire would invoke it again (double leader)")
	require.Equal(t, int32(1), locker.acquireN.Load(),
		"Run must not call Acquire again after a drain timeout")
}

// TestHoldLeadership_ProbeTimeoutTreatedAsLoss pins the bounded
// health-check probe: a probe that hangs (silent network drop) must be
// abandoned within the health-check window and treated as a leadership
// loss, instead of blocking the elector for the OS TCP retransmit
// timeout (minutes).
func TestHoldLeadership_ProbeTimeoutTreatedAsLoss(t *testing.T) {
	e := &Elector{
		key:           "leader",
		healthCheck:   20 * time.Millisecond,
		drainWarnTick: time.Hour,
		logger:        slog.Default(),
	}
	handle := &slowExtendHandle{}

	done := make(chan error, 1)
	go func() {
		done <- e.holdLeadership(context.Background(), handle, leaderelection.Callbacks{
			OnAcquired: func(ctx context.Context) { <-ctx.Done() },
		})
	}()

	select {
	case err := <-done:
		require.Error(t, err)
		require.ErrorContains(t, err, "extend",
			"a probe that overruns the health-check window must surface as an extend loss")
	case <-time.After(time.Second):
		t.Fatal("holdLeadership did not return — the hung probe was not bounded by a per-probe deadline")
	}
}

// TestHoldLeadership_IsLeaderFalseDuringDrain pins the IsLeader
// semantics during a callback drain: once leadership is factually lost,
// IsLeader must stop reporting true even though the OnAcquired callback
// is still draining. Otherwise a long-running callback that gates
// per-tick work on IsLeader keeps doing leader work while another
// replica already leads.
func TestHoldLeadership_IsLeaderFalseDuringDrain(t *testing.T) {
	e := &Elector{
		key:           "leader",
		healthCheck:   5 * time.Millisecond,
		drainWarnTick: time.Hour,
		logger:        slog.Default(),
	}
	// Simulate Run having marked us leader before the loss.
	e.leader.Store(true)

	handle := &fakeLockHandle{}
	handle.extendOK.Store(false) // force loss

	cancelled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })

	done := make(chan error, 1)
	go func() {
		done <- e.holdLeadership(context.Background(), handle, leaderelection.Callbacks{
			OnAcquired: func(ctx context.Context) {
				<-ctx.Done()
				close(cancelled)
				<-release // hold the drain open so we can observe IsLeader
			},
		})
	}()

	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("OnAcquired was not cancelled after loss")
	}

	// Loss is detected and the callback is still draining. IsLeader must
	// already report false.
	require.Eventually(t, func() bool { return !e.IsLeader() }, 200*time.Millisecond, 5*time.Millisecond,
		"IsLeader must be false once leadership is lost, even while the callback drains")

	releaseOnce.Do(func() { close(release) })
	select {
	case err := <-done:
		require.ErrorContains(t, err, "handle reports lost")
	case <-time.After(time.Second):
		t.Fatal("holdLeadership did not return after callback drained")
	}
}
