package outbox

import (
	"github.com/bds421/rho-kit/observability/promutil"
	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds Prometheus collectors for the outbox relay.
type Metrics struct {
	pendingCount   prometheus.Gauge
	relayLatency   prometheus.Histogram
	publishedTotal prometheus.Counter
	errorsTotal    prometheus.Counter
}

// MetricsOption configures Metrics.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer sets a custom Prometheus registerer. Default:
// prometheus.DefaultRegisterer.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	return func(c *metricsConfig) {
		c.registerer = reg
	}
}

// NewMetrics creates and registers Prometheus metrics for the outbox relay.
// Uses promutil.RegisterCollector for safe idempotent registration.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := &metricsConfig{
		registerer: prometheus.DefaultRegisterer,
	}
	for _, o := range opts {
		o(cfg)
	}

	m := &Metrics{
		pendingCount: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "outbox",
			Name:      "pending_count",
			Help:      "Number of outbox entries waiting to be published.",
		}),
		relayLatency: prometheus.NewHistogram(prometheus.HistogramOpts{
			Namespace: "outbox",
			Name:      "relay_latency_seconds",
			Help:      "Time to publish a single outbox entry to the broker.",
			Buckets:   prometheus.DefBuckets,
		}),
		publishedTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "outbox",
			Name:      "published_total",
			Help:      "Total number of outbox entries successfully published.",
		}),
		errorsTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "outbox",
			Name:      "errors_total",
			Help:      "Total number of outbox publish errors (includes retries).",
		}),
	}

	promutil.RegisterCollector(cfg.registerer, m.pendingCount)
	promutil.RegisterCollector(cfg.registerer, m.relayLatency)
	promutil.RegisterCollector(cfg.registerer, m.publishedTotal)
	promutil.RegisterCollector(cfg.registerer, m.errorsTotal)

	return m
}
