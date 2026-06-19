package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testComponent is a simple component for testing.
type testComponent struct {
	started  atomic.Bool
	stopped  atomic.Bool
	startErr error
	stopErr  error
	blockCh  chan struct{} // closed to unblock Start
}

func newTestComponent() *testComponent {
	return &testComponent{blockCh: make(chan struct{})}
}

func (c *testComponent) Start(ctx context.Context) error {
	c.started.Store(true)
	if c.startErr != nil {
		return c.startErr
	}
	select {
	case <-ctx.Done():
		return nil
	case <-c.blockCh:
		return nil
	}
}

func (c *testComponent) Stop(_ context.Context) error {
	c.stopped.Store(true)
	return c.stopErr
}

func TestRunner_CleanShutdown(t *testing.T) {
	logger := slog.Default()
	r := NewRunner(logger)

	c1 := newTestComponent()
	c2 := newTestComponent()
	r.Add("c1", c1)
	r.Add("c2", c2)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	// Wait for components to start (canary on the started atomics rather
	// than a fixed sleep, so the cancel below cannot race ahead of Start).
	require.Eventually(t, func() bool {
		return c1.started.Load() && c2.started.Load()
	}, time.Second, 5*time.Millisecond)

	// Cancel triggers shutdown
	cancel()

	err := <-done
	require.NoError(t, err)
	assert.True(t, c1.stopped.Load())
	assert.True(t, c2.stopped.Load())
}

func TestRunner_ComponentError(t *testing.T) {
	logger := slog.Default()
	r := NewRunner(logger)

	expectedErr := errors.New("component failed")
	c1 := newTestComponent()
	c1.startErr = expectedErr

	c2 := newTestComponent()
	r.Add("failing", c1)
	r.Add("healthy", c2)

	err := r.Run(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "component failed")
}

func TestRunner_ComponentCleanExitStopsOthers(t *testing.T) {
	r := NewRunner(slog.Default())
	other := newTestComponent()
	startedFinite := make(chan struct{})

	r.AddFunc("finite", func(context.Context) error {
		close(startedFinite)
		return nil
	})
	r.Add("other", other)

	done := make(chan error, 1)
	go func() {
		done <- r.Run(context.Background())
	}()

	select {
	case <-startedFinite:
	case <-time.After(time.Second):
		t.Fatal("finite component did not start")
	}

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Run hung after clean component exit")
	}
	assert.True(t, other.stopped.Load(), "clean component exit must trigger coordinated shutdown")
}

func TestRunner_AllComponentsCleanExitDoesNotHang(t *testing.T) {
	r := NewRunner(slog.Default())
	r.AddFunc("finite", func(context.Context) error { return nil })

	done := make(chan error, 1)
	go func() {
		done <- r.Run(context.Background())
	}()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Run hung after all components returned nil")
	}
}

func TestRunner_RunRejectsNilContext(t *testing.T) {
	err := NewRunner(slog.Default()).Run(nil) //nolint:staticcheck // exercising the explicit nil-context guard
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestRunner_AddFunc(t *testing.T) {
	logger := slog.Default()
	r := NewRunner(logger)

	started := atomic.Bool{}
	r.AddFunc("worker", func(ctx context.Context) error {
		started.Store(true)
		<-ctx.Done()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	require.Eventually(t, started.Load, time.Second, 5*time.Millisecond)

	cancel()
	err := <-done
	require.NoError(t, err)
}

func TestNewRunner_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		NewRunner(slog.Default(), nil)
	})
}

func TestWithBeforeStop_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() {
		WithBeforeStop(nil)
	})
}

// ctxAwareStopComponent blocks in Stop until its context is cancelled,
// recording whether it observed cancellation and how long Stop ran. It is
// the inverse of testComponent (whose Stop returns instantly) and is the
// only way to actually exercise the Runner's stop budget / salvage path.
type ctxAwareStopComponent struct {
	started        atomic.Bool
	stopObservedCt atomic.Bool  // set true if Stop saw ctx.Done()
	stopElapsed    atomic.Int64 // nanoseconds Stop blocked before returning
}

func (c *ctxAwareStopComponent) Start(ctx context.Context) error {
	c.started.Store(true)
	<-ctx.Done()
	return nil
}

func (c *ctxAwareStopComponent) Stop(ctx context.Context) error {
	start := time.Now()
	<-ctx.Done()
	c.stopObservedCt.Store(true)
	c.stopElapsed.Store(int64(time.Since(start)))
	return nil
}

// TestRunner_StopTimeout exercises the actual stop budget: a component whose
// Stop blocks until its context is cancelled must observe that cancellation
// once the stopTimeout budget is exhausted, and the whole shutdown must
// complete within a bound close to that budget (NOT block forever).
func TestRunner_StopTimeout(t *testing.T) {
	const budget = 100 * time.Millisecond
	r := NewRunner(slog.Default(), WithStopTimeout(budget))

	c1 := &ctxAwareStopComponent{}
	r.Add("slow-stop", c1)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	require.Eventually(t, c1.started.Load, time.Second, 5*time.Millisecond)

	shutdownStart := time.Now()
	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within bound — stop budget not enforced")
	}
	elapsed := time.Since(shutdownStart)

	assert.True(t, c1.stopObservedCt.Load(),
		"Stop must observe ctx.Done() driven by the stop budget")
	// The single component gets the full budget (perStepMinimum clamps it up
	// to 1s; budget here is below that, so stepCtx is bounded by sharedCtx =
	// budget). Allow generous slack for scheduling but assert it is bounded.
	assert.Less(t, elapsed, time.Second,
		"shutdown must be bounded by the stop budget, took %s", elapsed)
}

// TestRunner_SalvageHonorsHardCeiling pins the documented contract that
// stopTimeout is a HARD ceiling: once the shared budget is exhausted by an
// earlier component, the next component's Stop must observe ctx.Done()
// IMMEDIATELY rather than being granted a fresh per-component salvage budget.
// Before the fix the salvage context was derived from a non-cancelled parent
// with a fresh 1s timer, so a ctx-respecting Stop blocked ~1s past the
// ceiling — this test would have failed.
func TestRunner_SalvageHonorsHardCeiling(t *testing.T) {
	const budget = 80 * time.Millisecond
	r := NewRunner(slog.Default(), WithStopTimeout(budget))

	// hog is stopped FIRST (last registered) and consumes the whole budget.
	hog := &ctxAwareStopComponent{}
	// salvaged is stopped AFTER the budget is gone; its Stop respects ctx.
	salvaged := &ctxAwareStopComponent{}

	r.Add("salvaged", salvaged)
	r.Add("hog", hog)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	require.Eventually(t, func() bool {
		return hog.started.Load() && salvaged.started.Load()
	}, time.Second, 5*time.Millisecond)

	cancel()

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return — salvage path may be blocking")
	}

	require.True(t, salvaged.stopObservedCt.Load(), "salvaged Stop must run")
	// The salvaged component sees an already-cancelled context, so it must
	// return essentially immediately — well under the old 1s salvage budget.
	salvageWait := time.Duration(salvaged.stopElapsed.Load())
	assert.Less(t, salvageWait, 200*time.Millisecond,
		"salvaged Stop must observe ctx.Done() at once (hard ceiling), waited %s", salvageWait)
}

func TestWithStopTimeout_PanicsOnNonPositive(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		t.Run(d.String(), func(t *testing.T) {
			assert.Panics(t, func() {
				WithStopTimeout(d)
			})
		})
	}
}

func TestNewHTTPServer_NilPanics(t *testing.T) {
	assert.Panics(t, func() {
		NewHTTPServer(nil)
	})
}

func TestNewHTTPServer_PanicsOnUnsafeServer(t *testing.T) {
	safe := func() *http.Server {
		return &http.Server{
			Addr:              "127.0.0.1:0",
			Handler:           http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
			ReadHeaderTimeout: time.Second,
		}
	}

	tests := map[string]func(*http.Server){
		"empty addr":                  func(s *http.Server) { s.Addr = "" },
		"nil handler":                 func(s *http.Server) { s.Handler = nil },
		"missing read header timeout": func(s *http.Server) { s.ReadHeaderTimeout = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			srv := safe()
			mutate(srv)
			assert.Panics(t, func() {
				NewHTTPServer(srv)
			})
		})
	}
}

func TestNewHTTPServer_StopRejectsNilContext(t *testing.T) {
	var ctx context.Context
	err := NewHTTPServer(&http.Server{
		Addr:              "127.0.0.1:0",
		Handler:           http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		ReadHeaderTimeout: time.Second,
	}).Stop(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestFuncComponent_StartRejectsNilContext(t *testing.T) {
	fc := NewFuncComponent(func(ctx context.Context) error {
		return nil
	})
	var ctx context.Context
	err := fc.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestFuncComponent_Stop(t *testing.T) {
	fc := NewFuncComponent(func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	})
	err := fc.Stop(context.Background())
	assert.NoError(t, err)
}

func TestFuncComponent_StartRejectsSecondStart(t *testing.T) {
	started := make(chan struct{})
	fc := NewFuncComponent(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- fc.Start(ctx) }()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("FuncComponent did not start")
	}

	err := fc.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")

	cancel()
	require.NoError(t, <-done)
}

func TestFuncComponent_StartRejectsAfterStopBeforeStart(t *testing.T) {
	fc := NewFuncComponent(func(ctx context.Context) error {
		return nil
	})

	require.NoError(t, fc.Stop(context.Background()))

	err := fc.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already stopped")
}

func TestNewFuncComponent_PanicsOnNilFn(t *testing.T) {
	assert.Panics(t, func() {
		_ = NewFuncComponent(nil)
	})
}

func TestFuncComponent_StopRejectsNilContext(t *testing.T) {
	fc := NewFuncComponent(func(ctx context.Context) error {
		return nil
	})
	var ctx context.Context
	err := fc.Stop(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

// Wave 145: a panicking startFn must surface as an error from Start
// rather than propagating past the Runner. Previously the panic
// crashed the service; sibling components never got their Stop call.
func TestFuncComponent_PanicSurfacesAsError(t *testing.T) {
	fc := NewFuncComponent(func(ctx context.Context) error {
		panic("startFn boom")
	})
	err := fc.Start(context.Background())
	require.Error(t, err, "panicking startFn must surface as Start error")
	assert.Contains(t, err.Error(), "panicked")
	// Payload must NOT leak the panic value verbatim.
	assert.NotContains(t, err.Error(), "boom")
}

// orderedComponent records its name to a shared slice when Stop is called,
// allowing tests to verify shutdown order.
type orderedComponent struct {
	name     string
	mu       *sync.Mutex
	stopLog  *[]string
	startErr error
	started  atomic.Bool
}

func (c *orderedComponent) Start(ctx context.Context) error {
	if c.startErr != nil {
		return c.startErr
	}
	c.started.Store(true)
	<-ctx.Done()
	return nil
}

func (c *orderedComponent) Stop(_ context.Context) error {
	c.mu.Lock()
	*c.stopLog = append(*c.stopLog, c.name)
	c.mu.Unlock()
	return nil
}

func TestRunner_ReverseOrderShutdown(t *testing.T) {
	logger := slog.Default()
	r := NewRunner(logger)

	var mu sync.Mutex
	stopLog := make([]string, 0, 3)

	compA := &orderedComponent{name: "A", mu: &mu, stopLog: &stopLog}
	compB := &orderedComponent{name: "B", mu: &mu, stopLog: &stopLog}
	compC := &orderedComponent{name: "C", mu: &mu, stopLog: &stopLog}

	r.Add("A", compA)
	r.Add("B", compB)
	r.Add("C", compC)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	// Wait for every component to reach its blocking <-ctx.Done() select
	// before triggering shutdown (canary instead of a fixed sleep).
	require.Eventually(t, func() bool {
		return compA.started.Load() && compB.started.Load() && compC.started.Load()
	}, time.Second, 5*time.Millisecond)

	cancel()

	if err := <-done; err != nil {
		t.Fatalf("Run returned unexpected error: %v", err)
	}

	mu.Lock()
	got := make([]string, len(stopLog))
	copy(got, stopLog)
	mu.Unlock()

	want := []string{"C", "B", "A"}
	if len(got) != len(want) {
		t.Fatalf("expected %d stopped components, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("stop order mismatch at index %d: want %q, got %q (full order: %v)", i, want[i], got[i], got)
		}
	}
}

// panicComponent panics inside Start to exercise the Runner's panic recovery.
type panicComponent struct{}

func (p *panicComponent) Start(_ context.Context) error {
	panic("something went very wrong")
}

func (p *panicComponent) Stop(_ context.Context) error { return nil }

func TestRunner_AddPanicsOnInvalidArgs(t *testing.T) {
	r := NewRunner(slog.Default())
	c := newTestComponent()
	assert.PanicsWithValue(t, "lifecycle: Runner.Add requires a non-empty name", func() {
		r.Add("", c)
	})
	assert.PanicsWithValue(t, "lifecycle: Runner.Add requires a non-nil component", func() {
		r.Add("name", nil)
	})
}

func TestRunner_AddFuncPanicsOnNilFn(t *testing.T) {
	r := NewRunner(slog.Default())
	assert.PanicsWithValue(t, "lifecycle: NewFuncComponent requires a non-nil function", func() {
		r.AddFunc("name", nil)
	})
	assert.PanicsWithValue(t, "lifecycle: Runner.Add requires a non-empty name", func() {
		r.AddFunc("", func(ctx context.Context) error { return nil })
	})
}

func TestRunner_PanicRecovery(t *testing.T) {
	logger := slog.Default()
	r := NewRunner(logger)
	r.Add("panicker-secret-token", &panicComponent{})

	err := r.Run(context.Background())
	if err == nil {
		t.Fatal("expected Run to return a non-nil error after panic, got nil")
	}

	panicValue := "something went very wrong"
	if strings.Contains(err.Error(), panicValue) {
		t.Errorf("error %q leaks panic value %q", err.Error(), panicValue)
	}
	if !strings.Contains(err.Error(), "<redacted panic value: string>") {
		t.Errorf("error %q does not contain redacted panic marker", err.Error())
	}

	if strings.Contains(err.Error(), "panicker-secret-token") || strings.Contains(err.Error(), "secret-token") {
		t.Errorf("error %q leaks component name", err.Error())
	}

	// Consume the variable to satisfy errcheck / staticcheck.
	_ = fmt.Sprintf("%v", err)
}

func TestRunner_BeforeStopPanicReturnedAsError(t *testing.T) {
	r := NewRunner(slog.Default(), WithBeforeStop(func(context.Context) {
		panic("hook failed")
	}))
	c := newTestComponent()
	r.Add("worker", c)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- r.Run(ctx)
	}()

	require.Eventually(t, c.started.Load, time.Second, 10*time.Millisecond)
	cancel()

	err := <-done
	require.Error(t, err)
	assert.Contains(t, err.Error(), "BeforeStop panicked")
	assert.True(t, c.stopped.Load(), "component teardown must still run after hook panic")
}

// observableStopComponent records when Stop is called and lets the test
// observe the log output emitted BEFORE Stop runs. The signalStarted
// channel closes when the component is reached by stopAll; stopUntil
// blocks Stop until the test closes it. Without that pause the runner
// would emit "stopping component" and "component stopped" back-to-back
// and the ordering assertion would be too tight to be useful.
type observableStopComponent struct {
	started       atomic.Bool
	stopCalled    chan struct{}
	releaseStop   chan struct{}
	logAtStopOnce sync.Once
	logSnapshot   []byte
	logSrc        *bytes.Buffer
	logMu         *sync.Mutex
}

func (c *observableStopComponent) Start(ctx context.Context) error {
	c.started.Store(true)
	<-ctx.Done()
	return nil
}

func (c *observableStopComponent) Stop(_ context.Context) error {
	c.logAtStopOnce.Do(func() {
		c.logMu.Lock()
		c.logSnapshot = append([]byte(nil), c.logSrc.Bytes()...)
		c.logMu.Unlock()
		close(c.stopCalled)
	})
	<-c.releaseStop
	return nil
}

// TestRunner_StopEmitsStoppingLogBeforeStop verifies that "stopping
// component" is logged BEFORE the component's Stop method runs, so a
// hung Stop produces a usable diagnostic. The previous behaviour logged
// only "component stopped" AFTER Stop returned — useless when Stop
// itself was the problem.
func TestRunner_StopEmitsStoppingLogBeforeStop(t *testing.T) {
	var logMu sync.Mutex
	buf := &bytes.Buffer{}
	// Lock around every write so the snapshot inside Stop is race-free
	// — slog handlers can buffer internally and a parallel write would
	// otherwise interleave bytes between the snapshot and the assertion.
	syncBuf := &lockedWriter{w: buf, mu: &logMu}
	logger := slog.New(slog.NewTextHandler(syncBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	r := NewRunner(logger)

	c := &observableStopComponent{
		stopCalled:  make(chan struct{}),
		releaseStop: make(chan struct{}),
		logSrc:      buf,
		logMu:       &logMu,
	}
	r.Add("payment-worker", c)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	// Wait for the component to be in its Start blocking select (canary on
	// the started atomic), then trigger shutdown.
	require.Eventually(t, c.started.Load, time.Second, 5*time.Millisecond)
	cancel()

	select {
	case <-c.stopCalled:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop was never invoked")
	}

	// The "stopping component" message must already be in the log at
	// the moment Stop is entered — that is the whole point of the new
	// log line.
	snapshot := string(c.logSnapshot)
	if !strings.Contains(snapshot, "stopping component") {
		t.Fatalf("log at Stop entry missing %q line; got:\n%s", "stopping component", snapshot)
	}
	if !strings.Contains(snapshot, "component=payment-worker") {
		t.Fatalf("log at Stop entry missing component name; got:\n%s", snapshot)
	}

	close(c.releaseStop)
	require.NoError(t, <-done)

	logMu.Lock()
	final := buf.String()
	logMu.Unlock()

	// And the runner must still emit the existing "component stopped"
	// confirmation on the success path — the new log supplements, not
	// replaces, the existing terminal signal.
	if !strings.Contains(final, "component stopped") {
		t.Fatalf("final log missing %q; got:\n%s", "component stopped", final)
	}
}

// lockedWriter serialises writes to an underlying buffer so the test
// can snapshot it from another goroutine safely. bytes.Buffer is not
// safe for concurrent Read/Write.
type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// TestRunner_StopErrorLogIncludesElapsed verifies that the existing
// "component stop error" log gains an elapsed duration attribute. The
// elapsed field is the only signal the operator has for how long the
// failing Stop ran before giving up — without it, a Stop that fails
// after 100ms looks identical to one that fails after 29s.
func TestRunner_StopErrorLogIncludesElapsed(t *testing.T) {
	var logMu sync.Mutex
	buf := &bytes.Buffer{}
	syncBuf := &lockedWriter{w: buf, mu: &logMu}
	logger := slog.New(slog.NewTextHandler(syncBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	r := NewRunner(logger)
	c := newTestComponent()
	c.stopErr = errors.New("teardown rejected")
	r.Add("worker", c)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()

	require.Eventually(t, c.started.Load, time.Second, 5*time.Millisecond)
	cancel()

	err := <-done
	require.Error(t, err)
	assert.Contains(t, err.Error(), "teardown rejected")

	logMu.Lock()
	final := buf.String()
	logMu.Unlock()
	if !strings.Contains(final, "component stop error") {
		t.Fatalf("log missing %q; got:\n%s", "component stop error", final)
	}
	if !strings.Contains(final, "elapsed=") {
		t.Fatalf("log missing elapsed= attribute; got:\n%s", final)
	}
}
