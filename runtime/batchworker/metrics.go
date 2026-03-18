package batchworker

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/promutil"
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
			Buckets:   prometheus.DefBuckets,
		}, []string{"name"}),
	}
	promutil.RegisterCollector(reg, m.runs)
	promutil.RegisterCollector(reg, m.duration)
	return m
}
