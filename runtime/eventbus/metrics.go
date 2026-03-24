package eventbus

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/promutil"
)

// poolMetrics holds Prometheus metrics for the worker pool.
type poolMetrics struct {
	activeWorkers prometheus.Gauge
	queueDepth    prometheus.Gauge
	dropped       prometheus.Counter
	processed     *prometheus.CounterVec
}

// newPoolMetrics creates and registers pool metrics with the given registerer.
func newPoolMetrics(reg prometheus.Registerer) *poolMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &poolMetrics{
		activeWorkers: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "eventbus",
			Subsystem: "pool",
			Name:      "active_workers",
			Help:      "Number of worker goroutines currently processing events.",
		}),
		queueDepth: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "eventbus",
			Subsystem: "pool",
			Name:      "queue_depth",
			Help:      "Current number of events waiting in the worker pool queue.",
		}),
		dropped: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "eventbus",
			Name:      "events_dropped_total",
			Help:      "Total number of events dropped because the worker pool queue was full.",
		}),
		// WARNING: event_name must be a static developer-defined string. Using
		// dynamic values (request IDs, user IDs) causes unbounded Prometheus cardinality.
		processed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "eventbus",
			Name:      "events_processed_total",
			Help:      "Total number of events processed by the worker pool.",
		}, []string{"event_name"}),
	}

	promutil.RegisterCollector(reg, m.activeWorkers)
	promutil.RegisterCollector(reg, m.queueDepth)
	promutil.RegisterCollector(reg, m.dropped)
	promutil.RegisterCollector(reg, m.processed)

	return m
}
