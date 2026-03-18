package cron

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
			Namespace: "cron",
			Name:      "job_runs_total",
			Help:      "Total number of cron job executions.",
		}, []string{"name", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "cron",
			Name:      "job_duration_seconds",
			Help:      "Duration of cron job executions in seconds.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"name"}),
	}
	promutil.RegisterCollector(reg, m.runs)
	promutil.RegisterCollector(reg, m.duration)
	return m
}
