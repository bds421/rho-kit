package azurebackend

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Metrics holds Prometheus collectors for Azure Blob Storage operation
// monitoring.
type Metrics struct {
	opDuration *prometheus.HistogramVec
	opErrors   *prometheus.CounterVec
}

// MetricsOption configures the azurebackend metric constructor.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer. Unset defaults to
// [prometheus.DefaultRegisterer]; passing nil panics so a miswired
// caller surfaces at startup.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("azurebackend: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics creates and registers Azure Blob Storage metrics. Pass
// [WithRegisterer] for a non-default registry.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("azurebackend: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer

	m := &Metrics{
		opDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "storage",
				Subsystem: "azure",
				Name:      "operation_duration_seconds",
				Help:      "Duration of Azure Blob Storage operations.",
				Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
			[]string{"storage_instance", "operation"},
		),
		opErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "storage",
				Subsystem: "azure",
				Name:      "operation_errors_total",
				Help:      "Total Azure Blob Storage operation errors.",
			},
			[]string{"storage_instance", "operation"},
		),
	}

	m.opDuration = promutil.MustRegisterOrGet(reg, m.opDuration)
	m.opErrors = promutil.MustRegisterOrGet(reg, m.opErrors)

	return m
}

var defaultMetrics = sync.OnceValue(func() *Metrics { return NewMetrics() })

// now returns the current time. A variable so tests can override it.
var now = time.Now

func (m *Metrics) observeOp(instance, op string, start time.Time, err error) {
	m.opDuration.WithLabelValues(instance, op).Observe(time.Since(start).Seconds())
	if err != nil {
		m.opErrors.WithLabelValues(instance, op).Inc()
	}
}
