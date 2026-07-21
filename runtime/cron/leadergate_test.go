package cron

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func TestWithLeaderGate_SkipsWhenNotLeader(t *testing.T) {
	reg := prometheus.NewRegistry()
	var leader atomic.Bool // starts false → not leader
	s := New(nil, WithRegisterer(reg), WithLeaderGate(leader.Load))

	var ran atomic.Int32
	s.Add("gated-job", "@every 100ms", func(_ context.Context) error {
		ran.Add(1)
		return nil
	})

	runFirstJob(t, s)
	families, err := reg.Gather()
	require.NoError(t, err)
	require.GreaterOrEqual(t,
		metricValue(families, "cron_job_skipped_not_leader_total", map[string]string{"name": "gated-job"}),
		float64(1), "skipped counter must increment while not leader")

	require.Equal(t, int32(0), ran.Load(), "job must NOT run while gate denies")

	// Promote to leader; the next tick should run.
	leader.Store(true)
	runFirstJob(t, s)
	require.Equal(t, int32(1), ran.Load())
}

func TestWithLeaderGate_PanicSkipsJob(t *testing.T) {
	reg := prometheus.NewRegistry()
	s := New(nil, WithRegisterer(reg), WithLeaderGate(func() bool {
		panic("leader backend failed")
	}))

	var ran atomic.Int32
	s.Add("gated-job", "@every 1h", func(_ context.Context) error {
		ran.Add(1)
		return nil
	})

	entries := s.cron.Entries()
	require.NotEmpty(t, entries)
	require.NotPanics(t, func() {
		entries[0].Job.Run()
	})

	require.Equal(t, int32(0), ran.Load(), "job must not run when leader gate panics")
	families, err := reg.Gather()
	require.NoError(t, err)
	require.GreaterOrEqual(t,
		metricValue(families, "cron_job_skipped_not_leader_total", map[string]string{"name": "gated-job"}),
		float64(1),
	)
}

func TestWithLeaderGate_NoGateMeansAlwaysRun(t *testing.T) {
	// Sanity: without WithLeaderGate the scheduler runs unconditionally.
	reg := prometheus.NewRegistry()
	s := New(nil, WithRegisterer(reg))

	var ran atomic.Int32
	s.Add("always-job", "@every 50ms", func(_ context.Context) error {
		ran.Add(1)
		return nil
	})

	runFirstJob(t, s)
	require.Equal(t, int32(1), ran.Load())
}
