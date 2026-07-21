package vaulttransit

import (
	"errors"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds Vault Transit adapter collectors.
type Metrics struct {
	requestErrors *prometheus.CounterVec
}

// MetricsOption configures [NewMetrics].
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer selects the Prometheus registerer. The default is
// [prometheus.DefaultRegisterer].
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("vaulttransit: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics builds and registers Vault Transit error collectors.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("vaulttransit: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	errorsTotal := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "vaulttransit",
		Name:      "request_errors_total",
		Help:      "Total Vault Transit API errors observed by the adapter, labeled by HTTP status code and operation.",
	}, []string{"code", "operation"})
	if err := cfg.registerer.Register(errorsTotal); err != nil {
		var already prometheus.AlreadyRegisteredError
		if !errors.As(err, &already) {
			panic("vaulttransit: NewMetrics registration failed: " + err.Error())
		}
		existing, ok := already.ExistingCollector.(*prometheus.CounterVec)
		if !ok {
			panic("vaulttransit: NewMetrics found request_errors_total registered as an incompatible collector type")
		}
		errorsTotal = existing
	}
	return &Metrics{requestErrors: errorsTotal}
}

func (m *Metrics) recordError(operation, code string) {
	if m == nil {
		return
	}
	if code == "" {
		code = "unknown"
	}
	if operation == "" {
		operation = "unknown"
	}
	m.requestErrors.WithLabelValues(code, operation).Inc()
}

var (
	defaultMetricsOnce sync.Once
	defaultMetrics     *Metrics
)

func packageDefaultMetrics() *Metrics {
	defaultMetricsOnce.Do(func() { defaultMetrics = NewMetrics() })
	return defaultMetrics
}
