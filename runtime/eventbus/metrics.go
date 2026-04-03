package eventbus

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/promutil"
)

// busMetrics holds Prometheus metrics for the event bus.
type busMetrics struct {
	activeWorkers prometheus.Gauge
	dropped       prometheus.Counter
	processed     *prometheus.CounterVec
}

// newBusMetrics creates and registers metrics with the given registerer.
// Returns nil if reg is nil and no default registerer should be used
// (metrics are optional — nil metrics are checked at all callsites).
func newBusMetrics(reg prometheus.Registerer) *busMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &busMetrics{
		activeWorkers: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "eventbus",
			Subsystem: "pool",
			Name:      "active_workers",
			Help:      "Number of goroutines currently processing async events.",
		}),
		dropped: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "eventbus",
			Name:      "events_dropped_total",
			Help:      "Total number of async events dropped because the worker pool was full.",
		}),
		processed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "eventbus",
			Name:      "events_processed_total",
			Help:      "Total number of events processed by async handlers.",
		}, []string{"event_name"}),
	}

	promutil.RegisterCollector(reg, m.activeWorkers)
	promutil.RegisterCollector(reg, m.dropped)
	promutil.RegisterCollector(reg, m.processed)

	return m
}
