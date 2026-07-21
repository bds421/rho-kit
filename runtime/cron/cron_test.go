package cron

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduler_JobExecution(t *testing.T) {
	reg := prometheus.NewRegistry()
	s := New(nil, WithRegisterer(reg))

	var called atomic.Int32
	s.Add("test-job", "@every 100ms", func(_ context.Context) error {
		called.Add(1)
		return nil
	})

	runFirstJob(t, s)
	require.Equal(t, int32(1), called.Load())

	// Verify metrics were recorded.
	families, err := reg.Gather()
	require.NoError(t, err)
	found := metricValue(families, "cron_job_runs_total", map[string]string{"name": "test-job", "status": "success"})
	assert.GreaterOrEqual(t, found, float64(1))
}

func TestScheduler_JobError(t *testing.T) {
	reg := prometheus.NewRegistry()
	s := New(nil, WithRegisterer(reg))

	s.Add("fail-job", "@every 100ms", func(_ context.Context) error {
		return errors.New("boom")
	})

	runFirstJob(t, s)
	families, err := reg.Gather()
	require.NoError(t, err)
	require.GreaterOrEqual(t,
		metricValue(families, "cron_job_runs_total", map[string]string{"name": "fail-job", "status": "error"}),
		float64(1),
	)
}

func TestScheduler_PanicRecovery(t *testing.T) {
	reg := prometheus.NewRegistry()
	s := New(nil, WithRegisterer(reg))

	s.Add("panic-job", "@every 100ms", func(_ context.Context) error {
		panic("test panic")
	})

	require.NotPanics(t, func() { runFirstJob(t, s) })
	families, err := reg.Gather()
	require.NoError(t, err)
	require.GreaterOrEqual(t,
		metricValue(families, "cron_job_runs_total", map[string]string{"name": "panic-job", "status": "panic"}),
		float64(1),
	)
}

func TestScheduler_ContextCancelledOnStop(t *testing.T) {
	s := New(nil)

	var jobCtx atomic.Value
	s.Add("ctx-job", "@every 100ms", func(ctx context.Context) error {
		jobCtx.Store(ctx)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = s.Start(ctx) }()
	waitForSchedulerStarted(t, s)
	runFirstJob(t, s)
	require.NotNil(t, jobCtx.Load())

	cancel()
	_ = s.Stop(context.Background())

	// The context derived from the scheduler should be cancelled.
	stored := jobCtx.Load().(context.Context)
	assert.Error(t, stored.Err())
}

func TestScheduler_StartDrainsCronOnParentCtxCancel(t *testing.T) {
	// Start documents "blocks until ctx is cancelled". When a caller
	// cancels the parent ctx directly — without calling Scheduler.Stop —
	// the underlying robfig/cron run() goroutine and its timers must
	// still be drained, otherwise Start leaks a goroutine that loops
	// forever.
	s := New(nil)
	s.Add("leak-job", "@every 1h", func(_ context.Context) error { return nil })

	baseline := stableGoroutineCount(t)

	ctx, cancel := context.WithCancel(context.Background())
	startDone := make(chan error, 1)
	go func() { startDone <- s.Start(ctx) }()
	waitForSchedulerStarted(t, s)

	// Terminate via the documented path: cancel the parent ctx only.
	cancel()
	require.NoError(t, <-startDone)

	// The robfig run() goroutine must have exited; the goroutine count
	// should settle back to (around) the pre-Start baseline.
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseline
	}, 2*time.Second, 20*time.Millisecond,
		"cron run() goroutine should be drained after parent ctx cancel (baseline=%d, got=%d)",
		baseline, runtime.NumGoroutine())
}

// stableGoroutineCount returns a goroutine count that has stopped
// changing, so transient runtime/test goroutines don't skew the
// before/after comparison in leak tests.
func stableGoroutineCount(t *testing.T) int {
	t.Helper()
	prev := runtime.NumGoroutine()
	require.Eventually(t, func() bool {
		cur := runtime.NumGoroutine()
		if cur == prev {
			return true
		}
		prev = cur
		return false
	}, time.Second, 20*time.Millisecond)
	return prev
}

func TestScheduler_StartRejectsNilContext(t *testing.T) {
	s := New(nil)
	var ctx context.Context
	err := s.Start(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestScheduler_StartRejectsSecondStart(t *testing.T) {
	s := New(nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDone := make(chan error, 1)
	go func() { startDone <- s.Start(ctx) }()
	waitForSchedulerStarted(t, s)

	err := s.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")

	cancel()
	require.NoError(t, <-startDone)
}

func TestScheduler_StartRejectsRestartAfterStop(t *testing.T) {
	s := New(nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startDone := make(chan error, 1)
	go func() { startDone <- s.Start(ctx) }()
	waitForSchedulerStarted(t, s)

	require.NoError(t, s.Stop(context.Background()))
	require.NoError(t, <-startDone)

	err := s.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}

func TestScheduler_StartRejectsAfterStopBeforeStart(t *testing.T) {
	s := New(nil)

	require.NoError(t, s.Stop(context.Background()))

	err := s.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already stopped")
}

func TestScheduler_StopRejectsNilContext(t *testing.T) {
	s := New(nil)
	var ctx context.Context
	err := s.Stop(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-nil context")
}

func TestScheduler_InvalidSchedulePanics(t *testing.T) {
	s := New(nil)
	assert.Panics(t, func() {
		s.Add("bad", "not-a-cron-expr", func(_ context.Context) error { return nil })
	})
}

func TestScheduler_InvalidSchedulePanicDoesNotReflectInputs(t *testing.T) {
	s := New(nil)
	defer func() {
		rec := recover()
		require.NotNil(t, rec)
		msg, ok := rec.(string)
		require.True(t, ok, "panic must be a stable string, got %T", rec)
		assert.Contains(t, msg, "cron: Add invalid schedule for job:")
		// Parser diagnosis (field count) is safe to surface.
		assert.Contains(t, msg, "expected exactly 5 fields")
		// Job name and schedule expression must never appear.
		assert.NotContains(t, msg, "secret-token")
		assert.NotContains(t, msg, "not-a-cron-expr")
	}()

	s.Add("job-secret-token", "not-a-cron-expr-secret-token", func(_ context.Context) error { return nil })
}

func TestSanitizeCronParseError(t *testing.T) {
	assert.Equal(t, "expected exactly 5 fields, found 1",
		sanitizeCronParseError(errors.New("expected exactly 5 fields, found 1: [not-a-cron-expr-secret-token]")))
	assert.Equal(t, "failed to parse @every duration",
		sanitizeCronParseError(errors.New(`failed to parse duration @every xyz: time: invalid duration "xyz"`)))
}

func TestScheduler_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		New(nil, nil)
	})
}

func TestWithLocation_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() {
		WithLocation(nil)
	})
}

func TestWithLeaderGate_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() {
		WithLeaderGate(nil)
	})
}

func TestScheduler_AddPanicsOnNilFn(t *testing.T) {
	s := New(nil)
	assert.PanicsWithValue(t, "cron: Add requires a non-nil job function", func() {
		s.Add("name", "@every 1m", nil)
	})
}

func TestScheduler_AddPanicsOnEmptyName(t *testing.T) {
	s := New(nil)
	assert.PanicsWithValue(t, "cron: Add requires a non-empty name", func() {
		s.Add("", "@every 1m", func(_ context.Context) error { return nil })
	})
}

func TestScheduler_AddPanicsOnUnsafeName(t *testing.T) {
	tests := []string{
		"bad\nname",
		string([]byte{0xff}),
		strings.Repeat("a", 257),
	}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			s := New(nil)
			assert.Panics(t, func() {
				s.Add(name, "@every 1m", func(_ context.Context) error { return nil })
			})
		})
	}
}

func TestScheduler_DurationMetric(t *testing.T) {
	reg := prometheus.NewRegistry()
	s := New(nil, WithRegisterer(reg))

	s.Add("slow-job", "@every 100ms", func(_ context.Context) error {
		time.Sleep(10 * time.Millisecond)
		return nil
	})

	runFirstJob(t, s)
	families, err := reg.Gather()
	require.NoError(t, err)
	require.Greater(t,
		metricValue(families, "cron_job_duration_seconds", map[string]string{"name": "slow-job"}),
		float64(0),
	)
}

// metricValue finds a metric family by name and returns the value for a counter
// or histogram (sum) with the given label set.
func metricValue(families []*io_prometheus_client.MetricFamily, name string, labels map[string]string) float64 {
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			if matchLabels(m.GetLabel(), labels) {
				if c := m.GetCounter(); c != nil {
					return c.GetValue()
				}
				if h := m.GetHistogram(); h != nil {
					return h.GetSampleSum()
				}
			}
		}
	}
	return 0
}

func matchLabels(pairs []*io_prometheus_client.LabelPair, want map[string]string) bool {
	if len(want) == 0 {
		return true
	}
	got := make(map[string]string, len(pairs))
	for _, p := range pairs {
		got[p.GetName()] = p.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

func waitForSchedulerStarted(t *testing.T, s *Scheduler) {
	t.Helper()
	require.Eventually(t, func() bool {
		s.mu.RLock()
		started := s.started
		s.mu.RUnlock()
		return started
	}, time.Second, time.Millisecond)
}

func runFirstJob(t *testing.T, s *Scheduler) {
	t.Helper()
	entries := s.cron.Entries()
	require.NotEmpty(t, entries)
	entries[0].Job.Run()
}

func TestScheduler_SetJobTimeout_AppliesPerRunDeadline(t *testing.T) {
	reg := prometheus.NewRegistry()
	s := New(nil, WithRegisterer(reg))

	// We bypass the cron schedule here — SetJobTimeout configures a value
	// the wrapJob closure later observes. To test it directly, drive the
	// wrapped function manually.
	var seenErr atomic.Value
	s.Add("timed", "@every 1h", func(ctx context.Context) error {
		<-ctx.Done()
		seenErr.Store(ctx.Err())
		return ctx.Err()
	})
	s.SetJobTimeout("timed", 25*time.Millisecond)

	startCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = s.Start(startCtx) }()

	// Synthesise a tick by reaching into the underlying cron registry.
	entries := s.cron.Entries()
	require.NotEmpty(t, entries)
	entries[0].Job.Run()

	require.Eventually(t, func() bool {
		v := seenErr.Load()
		return v != nil && errors.Is(v.(error), context.DeadlineExceeded)
	}, time.Second, 5*time.Millisecond, "job context should hit DeadlineExceeded")
}

func TestScheduler_SetJobTimeout_PanicsOnNonPositive(t *testing.T) {
	// FR-094 [LOW]: SetJobTimeout used to silently no-op on
	// non-positive durations, leaving the job unbounded. It now
	// panics so the wiring bug surfaces.
	reg := prometheus.NewRegistry()
	s := New(nil, WithRegisterer(reg))
	assert.PanicsWithValue(t, "cron: SetJobTimeout requires d > 0", func() { s.SetJobTimeout("zero", 0) })
	assert.PanicsWithValue(t, "cron: SetJobTimeout requires d > 0", func() { s.SetJobTimeout("neg", -1*time.Second) })

	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.jobTimeouts["zero"]; ok {
		t.Fatal("zero duration should not be stored")
	}
	if _, ok := s.jobTimeouts["neg"]; ok {
		t.Fatal("negative duration should not be stored")
	}
}

func TestScheduler_SetJobTimeout_PanicsOnUnsafeName(t *testing.T) {
	s := New(nil)
	assert.Panics(t, func() {
		s.SetJobTimeout("bad\nname", time.Second)
	})

	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.jobTimeouts["bad\nname"]; ok {
		t.Fatal("unsafe job name should not be stored")
	}
}
