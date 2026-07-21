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

	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	rlock "github.com/bds421/rho-kit/data/lock/redislock/v2"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
)

// newValidLocker builds a non-nil rlock.Locker backed by miniredis so
// tests can exercise the NewWithLocker guards that run AFTER the
// locker-nil check. Passing rlock.NewLocker(nil) instead panics inside
// the locker constructor before NewWithLocker is ever reached, which
// makes the empty-key / nil-option guards untested.
func newValidLocker(t *testing.T) *rlock.Locker {
	t.Helper()
	mr := miniredis.RunT(t)
	t.Cleanup(mr.Close)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return rlock.NewLocker(client)
}

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

// runHold drives the elector's holdLeadership state machine directly
// against a fake lock handle — no Redis required. The parent context is
// generous (5s) so a loaded CI runner cannot trip a hidden parent
// deadline and divert the test onto the parent.Done() exit path instead
// of the renew-failure / cbDone path the caller intends to exercise.
//
// Tests that want to observe a return-before-completion (e.g. the
// callback-drains-first ordering) cancel via the callback's ctx or a
// false extendOK, not via this deadline.
func runHold(t *testing.T, e *Elector, handle *fakeLockHandle, cb leaderelection.Callbacks) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return e.holdLeadership(ctx, handle, cb)
}

func TestHoldLeadership_CallbackCompletesNormally(t *testing.T) {
	e := &Elector{
		renewInterval: 10 * time.Millisecond,
		logger:        nil,
	}
	// nil logger is fine since holdLeadership doesn't log success.
	handle := &fakeLockHandle{}
	handle.extendOK.Store(true)

	called := atomic.Bool{}
	cb := leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) {
			called.Store(true)
			// Return immediately; holdLeadership should observe cbDone.
		},
	}
	err := runHold(t, e, handle, cb)
	require.NoError(t, err)
	require.True(t, called.Load())
}

// drainHistogramCount returns the sample count of the drainDuration
// histogram series for the given key/state, or 0 if the series has no
// observations yet.
func drainHistogramCount(t *testing.T, m *Metrics, key, state string) uint64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 16)
	m.drainDuration.Collect(ch)
	close(ch)
	for metric := range ch {
		out := &dto.Metric{}
		require.NoError(t, metric.Write(out))
		var gotKey, gotState string
		for _, lp := range out.Label {
			switch lp.GetName() {
			case "key":
				gotKey = lp.GetValue()
			case "state":
				gotState = lp.GetValue()
			}
		}
		if gotKey == key && gotState == state && out.Histogram != nil {
			return out.Histogram.GetSampleCount()
		}
	}
	return 0
}

// TestHoldLeadership_HappyPathDoesNotRecordDrainMetric pins that a
// voluntary OnAcquired return (still leader) must NOT write whole-term
// duration into callback_drain_seconds{state=drained}. The drain
// histogram measures time waiting after leadership ended; on the happy
// path there is no drain wait. Recording time.Since(cbStart) inflated
// the SLO with term length (review-21).
func TestHoldLeadership_HappyPathDoesNotRecordDrainMetric(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))

	e := &Elector{
		key:           "tenant-sweeper",
		renewInterval: time.Hour, // never tick: force the cbDone happy path
		drainWarnTick: time.Hour,
		logger:        slog.Default(),
		metrics:       metrics,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(true)

	err := runHold(t, e, handle, leaderelection.Callbacks{
		OnAcquired: func(context.Context) {
			// Return immediately on the happy path.
		},
	})
	require.NoError(t, err)
	require.Equal(t, int32(0), handle.extendCt.Load(),
		"renew must not have ticked — this test must exercise the cbDone happy path")
	require.Equal(t, uint64(0), drainHistogramCount(t, metrics, "tenant-sweeper", drainStateDrained),
		"voluntary return must not inflate drain histogram with term length")
}

func TestHoldLeadership_RenewalFailureExits(t *testing.T) {
	e := &Elector{
		renewInterval: 10 * time.Millisecond,
	}
	handle := &fakeLockHandle{}
	handle.extendOK.Store(false) // simulate lost lock

	cb := leaderelection.Callbacks{
		OnAcquired: func(ctx context.Context) {
			// Block on ctx so the renewal failure is the exit condition.
			<-ctx.Done()
		},
	}
	err := runHold(t, e, handle, cb)
	// Assert the specific renew-loss path, not just "any error": a hidden
	// parent deadline (the old helper imposed 200ms) would surface
	// context.DeadlineExceeded here and still satisfy require.Error,
	// masking that the wrong exit path ran.
	require.ErrorContains(t, err, "handle reports lost")
	require.Positive(t, handle.extendCt.Load(), "Extend must have been attempted")
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

	err := runHold(t, e, handle, leaderelection.Callbacks{
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

	var callbackExited atomic.Bool
	err := runHold(t, e, handle, leaderelection.Callbacks{
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

	started := make(chan struct{})
	cancelled := make(chan struct{})
	released := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(released) })
	})

	result := make(chan error, 1)
	go func() {
		result <- runHold(t, e, handle, leaderelection.Callbacks{
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
	locker := newValidLocker(t)
	// require.PanicsWithValue cannot be used because the production guard
	// passes a plain string to panic; assert the message so a panic from
	// an unrelated cause (e.g. a nil locker) cannot make this pass.
	require.PanicsWithValue(t,
		"leaderelection/redislock: NewWithLocker key must not be empty",
		func() { NewWithLocker(locker, "") },
	)
}

func TestNewWithLocker_PanicsOnNilOption(t *testing.T) {
	locker := newValidLocker(t)
	require.PanicsWithValue(t,
		"leaderelection/redislock: NewWithLocker option must not be nil",
		func() { NewWithLocker(locker, "key", nil) },
	)
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
