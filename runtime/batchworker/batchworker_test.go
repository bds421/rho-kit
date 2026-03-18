package batchworker

import (
	"context"
	"errors"
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
	}, WithRegistry(reg), WithJitter(0))

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
	}, WithRegistry(reg), WithJitter(0))

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
	}, WithRegistry(reg), WithJitter(0))

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

func TestNew_PanicsOnInvalidArgs(t *testing.T) {
	assert.Panics(t, func() { New("", time.Second, func(ctx context.Context) error { return nil }) })
	assert.Panics(t, func() { New("x", 0, func(ctx context.Context) error { return nil }) })
	assert.Panics(t, func() { New("x", time.Second, nil) })
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
