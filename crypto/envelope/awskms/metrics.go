package awskms

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Metrics holds the awskms package's Prometheus collectors. Construct
// once at startup via [NewMetrics] and pass to [NewKEK] via
// [WithMetrics] so consumers can isolate awskms metrics on a custom
// registerer (the canonical kit pattern — see the registerer
// convention documented in AGENTS.md).
type Metrics struct {
	requestErrors *prometheus.CounterVec
}

// MetricsOption configures [NewMetrics].
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for awskms
// metrics. When unset, [prometheus.DefaultRegisterer] is used.
//
// Panics if reg is nil — a nil registerer would silently drop the
// collector registration and the operator would discover the gap
// only at scrape time.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("awskms: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics builds and registers the awskms collectors. Pass
// [WithRegisterer] for test isolation. Repeated NewMetrics calls
// against the same registerer reuse the existing collectors
// (Prometheus' AlreadyRegisteredError is unwrapped to the live
// collector) so test wiring that builds Metrics per subtest does not
// crash.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("awskms: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	// Wave 184: the `kit_` prefix was a v1-era anomaly; every other
	// kit metric uses Namespace=<domain>. Split as
	// Namespace="awskms", Name="request_errors_total" — wire form
	// shifts from kit_awskms_request_errors_total to
	// awskms_request_errors_total.
	requestErrors := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "awskms",
			Name:      "request_errors_total",
			Help:      "Total AWS KMS API errors observed by the awskms adapter, labeled by smithy error code and adapter operation.",
		},
		[]string{"code", "operation"},
	)
	if err := cfg.registerer.Register(requestErrors); err != nil {
		var are prometheus.AlreadyRegisteredError
		if as, ok := err.(prometheus.AlreadyRegisteredError); ok {
			are = as
			if existing, ok := are.ExistingCollector.(*prometheus.CounterVec); ok {
				requestErrors = existing
			}
		}
	}
	return &Metrics{requestErrors: requestErrors}
}

// recordError increments the request-error counter, mapping empty
// labels to "unknown" so the cardinality is bounded. Safe to call on
// a nil receiver — KEKs constructed without [WithMetrics] use the
// package-default Metrics under defaultMetricsOnce.
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

// defaultMetricsOnce builds a lazy DefaultRegisterer-backed Metrics
// the first time any KEK constructed without [WithMetrics] observes
// a KMS error. Operators who want a custom registerer must call
// [NewMetrics] + [WithMetrics] at startup BEFORE the first error.
var (
	defaultMetricsOnce sync.Once
	defaultMetrics     *Metrics
)

func packageDefaultMetrics() *Metrics {
	defaultMetricsOnce.Do(func() {
		defaultMetrics = NewMetrics()
	})
	return defaultMetrics
}
