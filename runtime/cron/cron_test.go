package cron

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

func TestScheduler_JobExecution(t *testing.T) {
	reg := prometheus.NewRegistry()
	s := New(nil, WithRegistry(reg))

	var called atomic.Int32
	s.Add("test-job", "@every 100ms", func(_ context.Context) error {
		called.Add(1)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = s.Start(ctx) }()

	// Wait for at least one execution.
	require.Eventually(t, func() bool { return called.Load() >= 1 }, 2*time.Second, 50*time.Millisecond)

	cancel()
	_ = s.Stop(context.Background())

	// Verify metrics were recorded.
	families, err := reg.Gather()
	require.NoError(t, err)
	found := metricValue(families, "cron_job_runs_total", map[string]string{"name": "test-job", "status": "success"})
	assert.GreaterOrEqual(t, found, float64(1))
}

func TestScheduler_JobError(t *testing.T) {
	reg := prometheus.NewRegistry()
	s := New(nil, WithRegistry(reg))

	s.Add("fail-job", "@every 100ms", func(_ context.Context) error {
		return errors.New("boom")
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = s.Start(ctx) }()

	require.Eventually(t, func() bool {
		families, _ := reg.Gather()
		return metricValue(families, "cron_job_runs_total", map[string]string{"name": "fail-job", "status": "error"}) >= 1
	}, 2*time.Second, 50*time.Millisecond)

	cancel()
	_ = s.Stop(context.Background())
}

func TestScheduler_PanicRecovery(t *testing.T) {
	reg := prometheus.NewRegistry()
	s := New(nil, WithRegistry(reg))

	s.Add("panic-job", "@every 100ms", func(_ context.Context) error {
		panic("test panic")
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = s.Start(ctx) }()

	require.Eventually(t, func() bool {
		families, _ := reg.Gather()
		return metricValue(families, "cron_job_runs_total", map[string]string{"name": "panic-job", "status": "panic"}) >= 1
	}, 2*time.Second, 50*time.Millisecond)

	cancel()
	_ = s.Stop(context.Background())
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

	require.Eventually(t, func() bool { return jobCtx.Load() != nil }, 2*time.Second, 50*time.Millisecond)

	cancel()
	_ = s.Stop(context.Background())

	// The context derived from the scheduler should be cancelled.
	stored := jobCtx.Load().(context.Context)
	assert.Error(t, stored.Err())
}

func TestScheduler_InvalidSchedulePanics(t *testing.T) {
	s := New(nil)
	assert.Panics(t, func() {
		s.Add("bad", "not-a-cron-expr", func(_ context.Context) error { return nil })
	})
}

func TestScheduler_DurationMetric(t *testing.T) {
	reg := prometheus.NewRegistry()
	s := New(nil, WithRegistry(reg))

	s.Add("slow-job", "@every 100ms", func(_ context.Context) error {
		time.Sleep(10 * time.Millisecond)
		return nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = s.Start(ctx) }()

	require.Eventually(t, func() bool {
		families, _ := reg.Gather()
		return metricValue(families, "cron_job_duration_seconds", map[string]string{"name": "slow-job"}) > 0
	}, 2*time.Second, 50*time.Millisecond)

	cancel()
	_ = s.Stop(context.Background())
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
