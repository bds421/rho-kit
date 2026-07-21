package batchworker

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWorker_RunsImmediatelyAndPeriodically(t *testing.T) {
	var count atomic.Int32
	reg := prometheus.NewRegistry()

	w := New("test-run", 50*time.Millisecond, func(ctx context.Context) error {
		count.Add(1)
		return nil
	}, WithRegisterer(reg), WithJitter(0))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()

	require.Eventually(t, func() bool {
		return count.Load() >= 3
	}, time.Second, 10*time.Millisecond, "expected at least 3 runs")

	cancel()
	require.NoError(t, <-done)

	// Verify metrics.
	mfs, err := reg.Gather()
	require.NoError(t, err)

	var foundRuns bool
	for _, mf := range mfs {
		if mf.GetName() == "batchworker_runs_total" {
			for _, m := range mf.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "status" && lp.GetValue() == "success" {
						foundRuns = true
						assert.GreaterOrEqual(t, m.GetCounter().GetValue(), float64(3))
					}
				}
			}
		}
	}
	assert.True(t, foundRuns, "expected batchworker_runs_total metric")
}

func TestWorker_ErrorLogged(t *testing.T) {
	var errCount atomic.Int32
	reg := prometheus.NewRegistry()

	w := New("test-error", 50*time.Millisecond, func(ctx context.Context) error {
		errCount.Add(1)
		return errors.New("batch failed")
	}, WithRegisterer(reg), WithJitter(0))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()

	require.Eventually(t, func() bool {
		return errCount.Load() >= 2
	}, time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-done)

	// Check error counter.
	mfs, err := reg.Gather()
	require.NoError(t, err)
	assertMetricLabel(t, mfs, "batchworker_runs_total", "status", "error")
}

func TestWorker_PanicRecovery(t *testing.T) {
	var count atomic.Int32
	reg := prometheus.NewRegistry()

	w := New("test-panic", 50*time.Millisecond, func(ctx context.Context) error {
		n := count.Add(1)
		if n == 1 {
			panic("boom")
		}
		return nil
	}, WithRegisterer(reg), WithJitter(0))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()

	// Worker should recover from panic and continue running.
	require.Eventually(t, func() bool {
		return count.Load() >= 3
	}, time.Second, 10*time.Millisecond, "expected recovery after panic")

	cancel()
	require.NoError(t, <-done)
}

func TestWorker_ContextCancellation(t *testing.T) {
	var count atomic.Int32

	w := New("test-cancel", time.Hour, func(ctx context.Context) error {
		count.Add(1)
		return nil
	}, WithJitter(0))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()

	// Wait for initial run.
	require.Eventually(t, func() bool {
		return count.Load() >= 1
	}, time.Second, 10*time.Millisecond)

	cancel()
	require.NoError(t, <-done)

	// Only 1 run (initial), since interval is 1 hour.
	assert.Equal(t, int32(1), count.Load())
}

func TestWorker_StartRejectsNilContext(t *testing.T) {
	w := New("test-start-nil-context", time.Hour, func(ctx context.Context) error {
		return nil
	}, WithJitter(0))

	var ctx context.Context
	err := w.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestWorker_StartRejectsSecondStart(t *testing.T) {
	started := make(chan struct{})
	var startedOnce sync.Once
	w := New("test-start-twice", time.Hour, func(ctx context.Context) error {
		startedOnce.Do(func() { close(started) })
		return nil
	}, WithJitter(0))

	startCtx, cancelStart := context.WithCancel(context.Background())
	startDone := make(chan error, 1)
	go func() { startDone <- w.Start(startCtx) }()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}

	err := w.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")

	cancelStart()
	require.NoError(t, <-startDone)
}

func TestWorker_StartRejectsRestartAfterStop(t *testing.T) {
	started := make(chan struct{})
	var startedOnce sync.Once
	w := New("test-restart-after-stop", time.Hour, func(ctx context.Context) error {
		startedOnce.Do(func() { close(started) })
		return nil
	}, WithJitter(0))

	startCtx, cancelStart := context.WithCancel(context.Background())
	defer cancelStart()
	startDone := make(chan error, 1)
	go func() { startDone <- w.Start(startCtx) }()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}

	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	require.NoError(t, w.Stop(stopCtx))
	require.NoError(t, <-startDone)

	err := w.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestWorker_Timeout(t *testing.T) {
	var timedOut atomic.Bool

	w := New("test-timeout", 50*time.Millisecond, func(ctx context.Context) error {
		select {
		case <-ctx.Done():
			timedOut.Store(true)
			return ctx.Err()
		case <-time.After(time.Hour):
			return nil
		}
	}, WithTimeout(20*time.Millisecond), WithJitter(0))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()

	require.Eventually(t, func() bool {
		return timedOut.Load()
	}, time.Second, 10*time.Millisecond, "expected batch to be timed out")

	cancel()
	require.NoError(t, <-done)
}

func TestWorker_StopReturnsCtxErrOnDeadline(t *testing.T) {
	// When the batch fn ignores cancellation (or runs longer than Stop's
	// deadline), Stop must surface ctx.Err() so callers know the worker
	// did not drain in time.
	release := make(chan struct{})
	running := make(chan struct{})
	w := New("test-stop-deadline", time.Hour, func(ctx context.Context) error {
		close(running)
		<-release
		return nil
	}, WithJitter(0), WithTimeout(time.Hour))

	startCtx, cancelStart := context.WithCancel(context.Background())
	startDone := make(chan error, 1)
	go func() { startDone <- w.Start(startCtx) }()
	defer func() {
		close(release)
		cancelStart()
		<-startDone
	}()

	<-running

	stopCtx, cancelStop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelStop()
	err := w.Stop(stopCtx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestWorker_StopBeforeStartReturnsImmediately(t *testing.T) {
	w := New("test-stop-before-start", time.Second, func(ctx context.Context) error {
		return nil
	}, WithJitter(0))

	// Stop before Start must not block: previously w.done was only closed
	// from Start, so this used to hang forever.
	stopDone := make(chan error, 1)
	go func() { stopDone <- w.Stop(context.Background()) }()
	select {
	case err := <-stopDone:
		assert.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("Stop before Start did not return immediately")
	}
}

func TestWorker_StartRejectsAfterStopBeforeStart(t *testing.T) {
	w := New("test-start-after-stop-before-start", time.Second, func(ctx context.Context) error {
		return nil
	}, WithJitter(0))

	require.NoError(t, w.Stop(context.Background()))

	err := w.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already stopped")
}

func TestWorker_StopRejectsNilContext(t *testing.T) {
	w := New("test-stop-nil-context", time.Second, func(ctx context.Context) error {
		return nil
	}, WithJitter(0))

	var ctx context.Context
	err := w.Stop(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestWorker_WithLoggerNilNormalizesToDefault(t *testing.T) {
	// WithLogger(nil) must not nil out the worker's logger; otherwise the
	// run path would panic on its first log call. The logger is normalized
	// back to slog.Default().
	w := New("test-nil-logger", 50*time.Millisecond, func(ctx context.Context) error {
		return nil
	}, WithLogger(nil), WithJitter(0))
	assert.NotNil(t, w.logger)
	assert.Same(t, slog.Default(), w.logger)
}

func TestNew_PanicsOnInvalidArgs(t *testing.T) {
	assert.Panics(t, func() { New("", time.Second, func(ctx context.Context) error { return nil }) })
	assert.Panics(t, func() { New("x", 0, func(ctx context.Context) error { return nil }) })
	assert.Panics(t, func() { New("x", time.Second, nil) })
	assert.Panics(t, func() { New("x", time.Second, func(ctx context.Context) error { return nil }, nil) })
	assert.PanicsWithValue(t, "batchworker: WithJitter requires 0 <= fraction <= 1", func() { WithJitter(1.1) })
	assert.PanicsWithValue(t, "batchworker: WithTimeout requires d > 0", func() { WithTimeout(-time.Second) })
}

func TestNew_PanicsOnUnsafeName(t *testing.T) {
	tests := []string{
		"bad\nname",
		string([]byte{0xff}),
		strings.Repeat("a", 257),
	}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Panics(t, func() {
				New(name, time.Second, func(ctx context.Context) error { return nil })
			})
		})
	}
}

func assertMetricLabel(t *testing.T, mfs []*io_prometheus_client.MetricFamily, name, labelName, labelValue string) {
	t.Helper()
	for _, mf := range mfs {
		if mf.GetName() == name {
			for _, m := range mf.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == labelName && lp.GetValue() == labelValue {
						return
					}
				}
			}
		}
	}
	t.Errorf("expected metric %s with label %s=%s", name, labelName, labelValue)
}

func TestWorker_StartPreCancelledCtxSkipsFirstBatch(t *testing.T) {
	calls := atomic.Int32{}
	w := New("pre-cancel", time.Hour, func(context.Context) error {
		calls.Add(1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := w.Start(ctx)
	require.NoError(t, err)
	assert.Equal(t, int32(0), calls.Load(), "pre-cancelled Start must not run a batch")
}
