package ratelimit

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

const (
	rateLimitKindIP    = "ip"
	rateLimitKindKeyed = "keyed"

	rateLimitOutcomeAllowed             = "allowed"
	rateLimitOutcomeLimited             = "limited"
	rateLimitOutcomeInvalidClientIP     = "invalid_client_ip"
	rateLimitOutcomeInvalidKey          = "invalid_key"
	rateLimitOutcomeUnavailable         = "unavailable"
	rateLimitOutcomeDegradedPassthrough = "degraded_passthrough"
	rateLimitOutcomeDegradedRejected    = "degraded_rejected"

	defaultLimiterName = "default"
)

// Metrics holds Prometheus collectors for rate-limit decisions.
//
// The label set is deliberately small: limiter is caller-provided and should
// be a static name such as "public_api" or "login", kind is "ip" or "keyed",
// and outcome is one of the package-defined outcome constants. Raw keys, IPs,
// tenant IDs, user IDs, and paths are never exported as labels.
type Metrics struct {
	decisions  *prometheus.CounterVec
	retryAfter *prometheus.HistogramVec
}

// MetricsOption configures rate-limit metrics construction.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for the rate-limit
// metrics. When unset, [prometheus.DefaultRegisterer] is used.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("ratelimit: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics creates and registers rate-limit metrics. Pass
// [WithRegisterer] to use a non-default registry. Repeated calls reuse
// already-registered collectors on the same registry.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("ratelimit: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	m := &Metrics{
		decisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "http",
			Subsystem: "ratelimit",
			Name:      "decisions_total",
			Help:      "Total rate-limit decisions by limiter, limiter kind, and outcome.",
		}, []string{"limiter", "kind", "outcome"}),
		retryAfter: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "http",
			Subsystem: "ratelimit",
			Name:      "retry_after_seconds",
			Help:      "Retry-After seconds emitted for rejected rate-limited requests.",
			Buckets:   []float64{1, 2, 5, 10, 30, 60, 300, 900, 3600},
		}, []string{"limiter", "kind"}),
	}
	m.decisions = promutil.MustRegisterOrGet(cfg.registerer, m.decisions)
	m.retryAfter = promutil.MustRegisterOrGet(cfg.registerer, m.retryAfter)
	return m
}

func (m *Metrics) observeDecision(limiter, kind, outcome string) {
	if m == nil {
		return
	}
	m.decisions.WithLabelValues(limiter, kind, outcome).Inc()
}

func (m *Metrics) observeRetryAfter(limiter, kind string, seconds float64) {
	if m == nil {
		return
	}
	m.retryAfter.WithLabelValues(limiter, kind).Observe(seconds)
}

func normalizeLimiterName(name string) string {
	if name == "" {
		return defaultLimiterName
	}
	if err := promutil.ValidateStaticLabelValue("limiter name", name); err != nil {
		panic("ratelimit: invalid limiter name")
	}
	return name
}
