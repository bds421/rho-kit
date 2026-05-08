package batchworker

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

type metrics struct {
	runs     *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func newMetrics(reg prometheus.Registerer) *metrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
	m := &metrics{
		runs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "batchworker",
			Name:      "runs_total",
			Help:      "Total number of batch worker executions.",
		}, []string{"name", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "batchworker",
			Name:      "duration_seconds",
			Help:      "Duration of batch worker executions in seconds.",
			// Batch workers routinely run for minutes; prometheus.DefBuckets
			// caps at 10s and would push every realistic batch into +Inf.
			Buckets: []float64{0.1, 1, 5, 10, 30, 60, 120, 300, 600, 1800, 3600},
		}, []string{"name"}),
	}
	promutil.RegisterCollector(reg, m.runs)
	promutil.RegisterCollector(reg, m.duration)
	return m
}
