package encryption

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Metrics holds optional Prometheus collectors for encryption open-plaintext
// budget acquire outcomes and wait latency.
type Metrics struct {
	openReaderAcquire *prometheus.CounterVec
	openReaderWait    prometheus.Histogram
}

// MetricsOption configures [NewMetrics].
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer for [NewMetrics]. Unset
// defaults to [prometheus.DefaultRegisterer]; passing nil panics so a
// miswired caller surfaces at startup.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("storage/encryption: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics creates and registers encryption metrics. Pass [WithRegisterer]
// for a non-default registry.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("storage/encryption: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer

	m := &Metrics{
		openReaderAcquire: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "storage",
				Subsystem: "encryption",
				Name:      "open_reader_acquire_total",
				Help:      "Total open-plaintext budget acquires by result (ok, timeout, canceled).",
			},
			[]string{"result"},
		),
		openReaderWait: prometheus.NewHistogram(
			prometheus.HistogramOpts{
				Namespace: "storage",
				Subsystem: "encryption",
				Name:      "open_reader_wait_seconds",
				Help:      "Time spent waiting to acquire the open-plaintext reader budget.",
				Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
		),
	}

	m.openReaderAcquire = promutil.MustRegisterOrGet(reg, m.openReaderAcquire)
	m.openReaderWait = promutil.MustRegisterOrGet(reg, m.openReaderWait)
	return m
}

// WithMetricsRegisterer enables open-plaintext budget metrics on
// [EncryptedStorage] and registers them on reg. Metrics are optional: when
// unset, acquire waits and rejections are not instrumented. Passing nil uses
// [prometheus.DefaultRegisterer].
func WithMetricsRegisterer(reg prometheus.Registerer) Option {
	return func(e *EncryptedStorage) {
		if reg == nil {
			e.metrics = NewMetrics()
			return
		}
		e.metrics = NewMetrics(WithRegisterer(reg))
	}
}

func (m *Metrics) observeOpenReaderAcquire(result string, waited time.Duration) {
	if m == nil {
		return
	}
	m.openReaderAcquire.WithLabelValues(result).Inc()
	m.openReaderWait.Observe(waited.Seconds())
}
