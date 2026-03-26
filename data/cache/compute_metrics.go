package cache

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/promutil"
)

// ComputeMetrics holds Prometheus collectors for ComputeCache monitoring.
type ComputeMetrics struct {
	hits        *prometheus.CounterVec
	misses      *prometheus.CounterVec
	staleServes *prometheus.CounterVec
	errors      *prometheus.CounterVec
}

// NewComputeMetrics creates and registers ComputeCache metrics with the given
// registerer. If reg is nil, prometheus.DefaultRegisterer is used.
func NewComputeMetrics(reg prometheus.Registerer) *ComputeMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &ComputeMetrics{
		hits: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "cache",
				Subsystem: "compute",
				Name:      "hits_total",
				Help:      "Total cache compute hits (fresh data returned).",
			},
			[]string{"name"},
		),
		misses: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "cache",
				Subsystem: "compute",
				Name:      "misses_total",
				Help:      "Total cache compute misses (computation triggered).",
			},
			[]string{"name"},
		),
		staleServes: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "cache",
				Subsystem: "compute",
				Name:      "stale_serves_total",
				Help:      "Total stale-while-revalidate serves.",
			},
			[]string{"name"},
		),
		errors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "cache",
				Subsystem: "compute",
				Name:      "errors_total",
				Help:      "Total errors during cache computation.",
			},
			[]string{"name"},
		),
	}

	promutil.RegisterCollector(reg, m.hits)
	promutil.RegisterCollector(reg, m.misses)
	promutil.RegisterCollector(reg, m.staleServes)
	promutil.RegisterCollector(reg, m.errors)

	return m
}

// WithComputePrometheusMetrics enables Prometheus metrics on the ComputeCache.
// If reg is nil, prometheus.DefaultRegisterer is used.
func WithComputePrometheusMetrics(reg prometheus.Registerer) ComputeOption {
	return func(cfg *computeConfig) {
		cfg.metrics = NewComputeMetrics(reg)
	}
}
