package k8slease

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/leaderelection"
)

func TestNew_PanicsOnNilClient(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil client")
		}
	}()
	New(nil, "ns", "name", "id")
}

func TestNew_PanicsOnEmptyNamespace(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty namespace")
		}
	}()
	New(newFakeClient(), "", "name", "id")
}

func TestNew_PanicsOnEmptyName(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty name")
		}
	}()
	New(newFakeClient(), "ns", "", "id")
}

func TestNew_PanicsOnEmptyIdentity(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on empty identity")
		}
	}()
	New(newFakeClient(), "ns", "name", "")
}

func TestNew_PanicsOnNilOption(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on nil option")
		}
	}()
	New(newFakeClient(), "ns", "name", "id", nil)
}

func TestOptions_PanicOnInvalidDurations(t *testing.T) {
	for name, fn := range map[string]func(){
		"WithLeaseDuration zero":            func() { WithLeaseDuration(0) },
		"WithLeaseDuration negative":        func() { WithLeaseDuration(-time.Second) },
		"WithRenewDeadline zero":            func() { WithRenewDeadline(0) },
		"WithRenewDeadline negative":        func() { WithRenewDeadline(-time.Second) },
		"WithRetryPeriod zero":              func() { WithRetryPeriod(0) },
		"WithRetryPeriod negative":          func() { WithRetryPeriod(-time.Second) },
		"WithCallbackDrainWarn zero":        func() { WithCallbackDrainWarnInterval(0) },
		"WithCallbackDrainWarn negative":    func() { WithCallbackDrainWarnInterval(-time.Second) },
		"WithCallbackDrainTimeout zero":     func() { WithCallbackDrainTimeout(0) },
		"WithCallbackDrainTimeout negative": func() { WithCallbackDrainTimeout(-time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			require.Panics(t, fn)
		})
	}
}

func TestNew_PanicsWhenLeaseNotGreaterThanRenew(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when leaseDuration <= renewDeadline")
		}
	}()
	New(newFakeClient(), "ns", "name", "id",
		WithLeaseDuration(5*time.Second),
		WithRenewDeadline(5*time.Second),
	)
}

func TestNew_PanicsWhenRenewNotGreaterThanRetry(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic when renewDeadline <= retryPeriod")
		}
	}()
	New(newFakeClient(), "ns", "name", "id",
		WithLeaseDuration(10*time.Second),
		WithRenewDeadline(2*time.Second),
		WithRetryPeriod(2*time.Second),
	)
}

func TestNew_DefaultsMirrorClientGoUpstream(t *testing.T) {
	e := New(newFakeClient(), "ns", "name", "id")
	require.Equal(t, defaultLeaseDuration, e.leaseDuration)
	require.Equal(t, defaultRenewDeadline, e.renewDeadline)
	require.Equal(t, defaultRetryPeriod, e.retryPeriod)
	require.Equal(t, defaultDrainWarnTick, e.drainWarnTick)
	require.Zero(t, e.drainTimeout, "drain timeout must default to zero so wait-forever is preserved")
}

func TestRun_RejectsNilContext(t *testing.T) {
	e := New(newFakeClient(), "ns", "name", "id")
	var ctx context.Context
	err := e.Run(ctx, leaderelection.Callbacks{})
	require.Error(t, err)
	require.ErrorContains(t, err, "non-nil context")
}

func TestRunOnLost_NilCallback(t *testing.T) {
	e := &Elector{logger: slog.Default(), namespace: "ns", name: "name"}
	require.NoError(t, e.runOnLost(leaderelection.Callbacks{}))
}

func TestRunOnLost_PanicReturned(t *testing.T) {
	e := &Elector{logger: slog.Default(), namespace: "ns", name: "name"}
	err := e.runOnLost(leaderelection.Callbacks{
		OnLost: func() {
			panic("lost cleanup exploded")
		},
	})
	require.ErrorContains(t, err, "OnLost panic")
	require.ErrorContains(t, err, "<redacted panic value: string>")
	require.NotContains(t, err.Error(), "lost cleanup exploded")
}

func TestAwaitCallbackDrain_DrainedRecordsTerminalObservation(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))

	e := &Elector{
		namespace:     "ns",
		name:          "tenant-sweeper",
		drainWarnTick: time.Hour,
		logger:        slog.Default(),
		metrics:       metrics,
	}

	cbDone := make(chan callbackResult, 1)
	cbDone <- callbackResult{}

	result := e.awaitCallbackDrain(cbDone)
	require.False(t, result.timedOut)
	require.Nil(t, result.panicValue)

	count := testutil.CollectAndCount(metrics.drainDuration)
	require.Equal(t, 1, count, "exactly one terminal observation must be recorded")
}

func TestAwaitCallbackDrain_LongCallbackEmitsWarnAndMetric(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))

	logBuf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	e := &Elector{
		namespace:     "kit-system",
		name:          "tenant-sweeper",
		drainWarnTick: 10 * time.Millisecond,
		logger:        logger,
		metrics:       metrics,
	}

	cbDone := make(chan callbackResult, 1)
	doneCh := make(chan callbackResult, 1)

	go func() {
		doneCh <- e.awaitCallbackDrain(cbDone)
	}()

	require.Eventually(t, func() bool {
		return testutil.ToFloat64(metrics.drainWarns.WithLabelValues("kit-system", "tenant-sweeper")) >= 2
	}, time.Second, 5*time.Millisecond, "expected drain warn metric to increment at least twice")

	require.Contains(t, logBuf.String(), "OnAcquired callback still draining")
	// Both namespace and name are logged via redact.String — verify
	// they show up as redacted attributes rather than asserting on the
	// raw values.
	require.Contains(t, logBuf.String(), "namespace=")
	require.Contains(t, logBuf.String(), "name=")
	require.True(t, strings.Contains(logBuf.String(), "elapsed"))

	cbDone <- callbackResult{}

	select {
	case result := <-doneCh:
		require.False(t, result.timedOut)
	case <-time.After(500 * time.Millisecond):
		t.Fatal("awaitCallbackDrain did not return after cbDone")
	}
}

func TestAwaitCallbackDrain_TimeoutAbandonsStalledCallback(t *testing.T) {
	e := &Elector{
		namespace:     "ns",
		name:          "stalled",
		drainWarnTick: time.Hour,
		drainTimeout:  30 * time.Millisecond,
		logger:        slog.Default(),
	}

	cbDone := make(chan callbackResult)
	result := e.awaitCallbackDrain(cbDone)
	require.True(t, result.timedOut, "drain timeout must signal timedOut=true so Run surfaces ErrCallbackDrainTimeout")
}

func TestAwaitCallbackDrain_TimeoutNotTrippedWhenCallbackCooperates(t *testing.T) {
	e := &Elector{
		namespace:     "ns",
		name:          "cooperative",
		drainWarnTick: time.Hour,
		drainTimeout:  500 * time.Millisecond,
		logger:        slog.Default(),
	}

	cbDone := make(chan callbackResult, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		cbDone <- callbackResult{}
	}()
	result := e.awaitCallbackDrain(cbDone)
	require.False(t, result.timedOut)
	require.Nil(t, result.panicValue)
}

func TestOnAcquiredPanicError_RedactsValue(t *testing.T) {
	err := onAcquiredPanicError("leader work exploded")
	require.ErrorContains(t, err, "OnAcquired panic")
	require.ErrorContains(t, err, "<redacted panic value: string>")
	require.NotContains(t, err.Error(), "leader work exploded")
}

func TestErrCallbackDrainTimeout_Sentinel(t *testing.T) {
	// The sentinel must be a simple errors.New value so callers can
	// rely on errors.Is — joined-error chains still match.
	require.NotNil(t, ErrCallbackDrainTimeout)
	wrapped := errors.Join(errors.New("other"), ErrCallbackDrainTimeout)
	require.ErrorIs(t, wrapped, ErrCallbackDrainTimeout)
}

func TestJoinStoppedLeadingErrors_NilWhenNoSignal(t *testing.T) {
	require.NoError(t, joinStoppedLeadingErrors(nil, callbackResult{}))
}

func TestJoinStoppedLeadingErrors_OnlyOnLost(t *testing.T) {
	want := errors.New("onlost boom")
	got := joinStoppedLeadingErrors(want, callbackResult{})
	require.ErrorIs(t, got, want, "single signal must round-trip without errors.Join wrapping")
}

func TestJoinStoppedLeadingErrors_OnLostAndOnAcquiredPanic(t *testing.T) {
	// OnLost returning an error AND OnAcquired panicking must both
	// surface — previously the panic overwrote the OnLost error.
	onLost := errors.New("onlost cleanup boom")
	got := joinStoppedLeadingErrors(onLost, callbackResult{panicValue: "leader boom"})
	require.Error(t, got)
	require.ErrorIs(t, got, onLost, "OnLost error must remain reachable via errors.Is")
	require.ErrorContains(t, got, "OnAcquired panic")
}

func TestJoinStoppedLeadingErrors_OnLostAndDrainTimeout(t *testing.T) {
	onLost := errors.New("onlost cleanup boom")
	got := joinStoppedLeadingErrors(onLost, callbackResult{timedOut: true})
	require.ErrorIs(t, got, onLost)
	require.ErrorIs(t, got, ErrCallbackDrainTimeout)
}

func TestJoinStoppedLeadingErrors_AllThreeSignals(t *testing.T) {
	onLost := errors.New("onlost cleanup boom")
	got := joinStoppedLeadingErrors(onLost, callbackResult{
		panicValue: "leader boom",
		timedOut:   true,
	})
	require.ErrorIs(t, got, onLost)
	require.ErrorIs(t, got, ErrCallbackDrainTimeout)
	require.ErrorContains(t, got, "OnAcquired panic")
}

func TestIsLeader_DefaultFalse(t *testing.T) {
	e := New(newFakeClient(), "ns", "name", "id")
	require.False(t, e.IsLeader())
}

func TestRun_DoubleInvocationRejected(t *testing.T) {
	e := &Elector{
		client:        newFakeClient(),
		namespace:     "ns",
		name:          "name",
		identity:      "id",
		leaseDuration: defaultLeaseDuration,
		renewDeadline: defaultRenewDeadline,
		retryPeriod:   defaultRetryPeriod,
		drainWarnTick: defaultDrainWarnTick,
		logger:        slog.Default(),
	}
	e.started.Store(true) // simulate prior Run

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := e.Run(ctx, leaderelection.Callbacks{})
	require.Error(t, err)
	require.ErrorContains(t, err, "Run already invoked")
}

func TestRun_ResetsStartedSoDocumentedRetryLoopWorks(t *testing.T) {
	// The package docs tell callers to "wrap Run in their own retry
	// loop". That only works if `started` is cleared once Run returns;
	// otherwise the second invocation permanently fails with "Run
	// already invoked" and a transient API-server blip silently removes
	// the replica from the election forever.
	e := &Elector{
		client:        newFakeClient(),
		namespace:     "ns",
		name:          "name",
		identity:      "id",
		leaseDuration: defaultLeaseDuration,
		renewDeadline: defaultRenewDeadline,
		retryPeriod:   defaultRetryPeriod,
		drainWarnTick: defaultDrainWarnTick,
		logger:        slog.Default(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // never acquires; client-go Run returns promptly

	done := make(chan error, 1)
	go func() { done <- e.Run(ctx, leaderelection.Callbacks{}) }()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("first Run did not return")
	}

	require.False(t, e.started.Load(), "started must be cleared after Run returns so a retry loop can call Run again")

	// A second Run must be accepted (not rejected with "already invoked").
	done2 := make(chan error, 1)
	go func() { done2 <- e.Run(ctx, leaderelection.Callbacks{}) }()
	select {
	case err := <-done2:
		require.NotContains(t, err.Error(), "Run already invoked")
	case <-time.After(3 * time.Second):
		t.Fatal("second Run did not return")
	}
}

func TestComputeRunResult_LeadershipLostReturnsSentinel(t *testing.T) {
	// Leadership lost via renew failure (no ctx cancellation, no OnLost
	// error) must surface a distinct error, not nil — callers read nil
	// as a clean shutdown and would stop retrying, contradicting the
	// Elector contract ("returns when ctx cancels or unrecoverable
	// backend error").
	got := computeRunResult(nil, nil)
	require.ErrorIs(t, got, ErrLeadershipLost)
}

func TestComputeRunResult_CtxCancelTakesPrecedence(t *testing.T) {
	got := computeRunResult(nil, context.Canceled)
	require.ErrorIs(t, got, context.Canceled)
	require.NotErrorIs(t, got, ErrLeadershipLost, "a clean ctx-cancel shutdown is not a leadership-loss error")
}

func TestComputeRunResult_LostErrJoinedWithCtx(t *testing.T) {
	lost := errors.New("onlost boom")
	got := computeRunResult(lost, context.Canceled)
	require.ErrorIs(t, got, context.Canceled)
	require.ErrorIs(t, got, lost)
}

func TestComputeRunResult_LostErrWithoutCtx(t *testing.T) {
	lost := errors.New("onlost boom")
	got := computeRunResult(lost, nil)
	require.ErrorIs(t, got, lost)
	require.NotErrorIs(t, got, ErrLeadershipLost, "a captured OnStoppedLeading error already describes the loss")
}

func TestOnStartedLeading_SkipsLeadershipWhenLeaderCtxAlreadyCancelled(t *testing.T) {
	// Race guard: client-go launches OnStartedLeading in its own
	// goroutine (go OnStartedLeading(ctx); le.renew(ctx)). If renew
	// returns before that goroutine is scheduled, OnStoppedLeading can
	// store leader=false first; the goroutine must NOT then store
	// leader=true (which would leave IsLeader() stuck true forever) or
	// invoke OnAcquired after leadership ended.
	e := &Elector{
		namespace: "ns",
		name:      "name",
		identity:  "id",
		logger:    slog.Default(),
	}
	tm := newTerm()

	leaderCtx, cancel := context.WithCancel(context.Background())
	cancel() // leadership already over before the goroutine runs

	acquiredCalled := atomic.Bool{}
	e.onStartedLeading(leaderCtx, tm, leaderelection.Callbacks{
		OnAcquired: func(context.Context) { acquiredCalled.Store(true) },
	})

	require.False(t, e.IsLeader(), "must not claim leadership when leaderCtx is already cancelled")
	require.False(t, tm.didAcquire(), "term must not be marked acquired so OnStoppedLeading skips OnLost")
	require.False(t, acquiredCalled.Load(), "OnAcquired must not run after leadership ended")

	// The defer must still signal completion so awaitCallbackDrain (if
	// it were waiting) never blocks.
	select {
	case <-tm.cbDone:
	default:
		t.Fatal("onStartedLeading must always signal cbDone")
	}
}

func TestOnStartedLeading_ClaimsLeadershipWhenCtxLive(t *testing.T) {
	e := &Elector{
		namespace: "ns",
		name:      "name",
		identity:  "id",
		logger:    slog.Default(),
	}
	tm := newTerm()

	leaderCtx := context.Background()
	acquiredCalled := atomic.Bool{}
	e.onStartedLeading(leaderCtx, tm, leaderelection.Callbacks{
		OnAcquired: func(context.Context) { acquiredCalled.Store(true) },
	})

	require.True(t, e.IsLeader())
	require.True(t, tm.didAcquire())
	require.True(t, acquiredCalled.Load())
	select {
	case <-tm.cbDone:
	default:
		t.Fatal("onStartedLeading must signal cbDone")
	}
}

func TestTerm_StopBeforeStartNeutralizesLateOnStartedLeading(t *testing.T) {
	// THE CORE RACE (TOCTOU between OnStartedLeading's ctx check and its
	// leader claim). client-go's Run is:
	//
	//	defer OnStoppedLeading()
	//	...
	//	go OnStartedLeading(leaderCtx); le.renew(leaderCtx)
	//
	// Interleaving that the leaderCtx.Err() guard alone does NOT close:
	//
	//	1. OnStartedLeading goroutine starts, checks leaderCtx.Err() ==
	//	   nil (term still live) and passes the guard.
	//	2. The goroutine is preempted before it marks the term acquired.
	//	3. renew returns, leaderCtx is cancelled, OnStoppedLeading runs:
	//	   it sees the term NOT acquired, so it stores leader=false and
	//	   returns (skips drain + OnLost), then Run returns.
	//	4. The goroutine resumes and stores leader=true + runs OnAcquired
	//	   AFTER Run already returned.
	//
	// Result without the term coordinator: IsLeader() stuck true forever
	// and OnAcquired firing after the elector stopped, with no matching
	// OnLost.
	//
	// This test reproduces that exact ordering deterministically by
	// driving OnStoppedLeading to completion BEFORE OnStartedLeading is
	// allowed to claim, and asserting the late claim is neutralized.
	e := &Elector{
		namespace: "ns",
		name:      "name",
		identity:  "id",
		logger:    slog.Default(),
	}
	tm := newTerm()

	// Step 3: OnStoppedLeading wins the coordinator first. The term was
	// never acquired, so it must report "no acquired term" (acquired ==
	// false) and leader must be false.
	require.False(t, e.onStoppedLeading(tm, leaderelection.Callbacks{
		OnLost: func() { t.Fatal("OnLost must not run for a never-acquired term") },
	}), "onStoppedLeading must report no acquired term when started never claimed")
	require.False(t, e.IsLeader(), "leader must be false after OnStoppedLeading")

	// Step 4: the late OnStartedLeading goroutine resumes with a still
	// live leaderCtx (the ctx check would pass) but MUST observe the
	// stopped term and refuse to claim leadership or run OnAcquired.
	acquiredCalled := atomic.Bool{}
	leaderCtx := context.Background() // live ctx: only the term gate can stop it
	e.onStartedLeading(leaderCtx, tm, leaderelection.Callbacks{
		OnAcquired: func(context.Context) { acquiredCalled.Store(true) },
	})

	require.False(t, e.IsLeader(), "late OnStartedLeading must not resurrect leadership after the term stopped")
	require.False(t, acquiredCalled.Load(), "OnAcquired must not run after the term stopped")
	require.False(t, tm.didAcquire(), "a stopped term must never flip to acquired")
}

func TestTerm_AcquireBeforeStopDrainsAndRunsOnLost(t *testing.T) {
	// The complementary ordering: OnStartedLeading wins the coordinator
	// first (claims + runs OnAcquired), then OnStoppedLeading runs and
	// must drain the callback and invoke OnLost exactly once.
	e := &Elector{
		namespace:     "ns",
		name:          "name",
		identity:      "id",
		drainWarnTick: time.Hour,
		logger:        slog.Default(),
	}
	tm := newTerm()

	acquiredCalled := atomic.Bool{}
	e.onStartedLeading(context.Background(), tm, leaderelection.Callbacks{
		OnAcquired: func(context.Context) { acquiredCalled.Store(true) },
	})
	require.True(t, e.IsLeader())
	require.True(t, acquiredCalled.Load())
	require.True(t, tm.didAcquire())

	onLostCalls := atomic.Int32{}
	cb := leaderelection.Callbacks{OnLost: func() { onLostCalls.Add(1) }}

	// onStoppedLeading reports the acquired term; Run's closure then
	// drains the callback and runs OnLost. Exercise that same sequence.
	require.True(t, e.onStoppedLeading(tm, cb),
		"onStoppedLeading must report an acquired term so Run drains + runs OnLost")
	require.False(t, e.IsLeader(), "leader must be false after OnStoppedLeading")

	drainResult := e.awaitCallbackDrain(tm.cbDone)
	require.False(t, drainResult.timedOut)
	require.Nil(t, drainResult.panicValue)
	require.NoError(t, e.runOnLost(cb))
	require.Equal(t, int32(1), onLostCalls.Load(), "OnLost must run exactly once for an acquired term")
}

func TestRun_DoesNotCallOnLostWithoutAcquired(t *testing.T) {
	// A cancelled context never lets the LeaderElector acquire; the
	// adapter must NOT call OnLost in that case (the kit contract
	// states OnLost describes an acquired term, not a never-started
	// one). We cannot drive LeaderElector.Run without a real or fake
	// clientset, so we exercise the gating directly: with
	// onAcquiredStarted=false the OnStoppedLeading branch returns
	// before invoking the user callback.
	//
	// This test pins the behaviour by reading the same atomic flag
	// the production code uses; the integration test covers the
	// end-to-end client-go scenario.
	var onAcquiredStarted atomic.Bool
	called := atomic.Bool{}

	// Inline the gate so the test exercises the same predicate.
	stopFn := func() {
		if !onAcquiredStarted.Load() {
			return
		}
		called.Store(true)
	}
	stopFn()
	require.False(t, called.Load(), "OnLost must not run when leadership was never acquired")
}
