// Cron emits its own RED metrics under the `cron_*` prefix:
//
//   - cron_job_runs_total{name, status}
//   - cron_job_duration_seconds{name}
//   - cron_job_skipped_not_leader_total{name}
//
// These metric names predate the kit's general-purpose
// [github.com/bds421/rho-kit/observability/v2/redmetrics] BatchMetrics
// which uses `cron_runs_total` / `cron_run_duration_seconds`. The two
// are intentionally NOT unified: existing services have dashboards
// pinned to `cron_job_*`, and the BatchMetrics shape is exposed as a
// generic constructor for batchworker / outbox / custom-runner code
// that doesn't have the same back-compat constraint.

package cron

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

type metrics struct {
	runs             *prometheus.CounterVec
	duration         *prometheus.HistogramVec
	skippedNotLeader *prometheus.CounterVec
}

func newMetrics(reg prometheus.Registerer) *metrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	m := &metrics{
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "cron",
			Name:      "job_runs_total",
			Help:      "Total number of cron job executions.",
		}, []string{"name", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "cron",
			Name:      "job_duration_seconds",
			Help:      "Duration of cron job executions in seconds.",
			// Wider buckets than prometheus.DefBuckets (which tops out at 10s).
			// Cron jobs commonly run for minutes; everything beyond 10s
			// landing in +Inf would make histogram_quantile useless.
			Buckets: []float64{0.1, 1, 5, 10, 30, 60, 120, 300, 600, 1800, 3600},
		}, []string{"name"}),
	}
	m.skippedNotLeader = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "cron",
		Name:      "job_skipped_not_leader_total",
		Help:      "Cron jobs skipped because the leader-election gate reported this replica is not the leader.",
	}, []string{"name"})
	promutil.RegisterCollector(reg, m.runs)
	promutil.RegisterCollector(reg, m.duration)
	promutil.RegisterCollector(reg, m.skippedNotLeader)
	return m
}
