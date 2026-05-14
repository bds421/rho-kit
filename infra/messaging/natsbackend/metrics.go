package natsbackend

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

const (
	natsPublishOutcomeSuccess        = "success"
	natsPublishOutcomeFailed         = "failed"
	natsPublishOutcomeInvalidMessage = "invalid_message"
	natsPublishOutcomeTooLarge       = "too_large"

	natsConsumeOutcomeAcked        = "acked"
	natsConsumeOutcomeAckFailed    = "ack_failed"
	natsConsumeOutcomeRetry        = "retry"
	natsConsumeOutcomeNakFailed    = "nak_failed"
	natsConsumeOutcomePermanent    = "permanent"
	natsConsumeOutcomeDecodeError   = "decode_error"
	natsConsumeOutcomeValidateError = "validate_error"
	natsConsumeOutcomeHandlerPanic  = "handler_panic"
	natsConsumeOutcomeTermFailed   = "term_failed"

	natsHandlerOutcomeSuccess = "success"
	natsHandlerOutcomeError   = "error"
	natsHandlerOutcomePanic   = "panic"
)

// Metrics holds Prometheus collectors for direct NATS JetStream publishing
// and consuming.
//
// Labels are intentionally restricted to topology-level dimensions:
// exchange, routing_key, stream, durable, and outcome. Do not encode tenant
// IDs, request IDs, user IDs, or payload-derived values into NATS topology
// names when these metrics are enabled.
type Metrics struct {
	published       *prometheus.CounterVec
	publishDuration *prometheus.HistogramVec
	consumed        *prometheus.CounterVec
	handlerDuration *prometheus.HistogramVec

	// labelRoute maps a raw (exchange, routingKey) pair to the label
	// values that reach Prometheus. Default is identity; opt in via
	// [WithOpaqueRouteLabels] to bound cardinality when a service
	// embeds tenant or resource IDs into the route segments.
	labelRoute routeLabelFunc
}

type routeLabelFunc func(exchange, routingKey string) (string, string)

func passthroughRouteLabel(exchange, routingKey string) (string, string) {
	return exchange, routingKey
}

// MetricsOption configures the NATS metric constructor. Standardised
// across the kit so every package exposes `NewMetrics(opts ...MetricsOption)`.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
	labelRoute routeLabelFunc
}

// WithRegisterer pins the Prometheus registerer used for NATS
// metrics. When unset, [prometheus.DefaultRegisterer] is used. Passing
// nil panics so a miswired "metrics enabled, registerer not supplied"
// caller surfaces at startup rather than going to the global default.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("natsbackend: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// WithOpaqueRouteLabels passes every (exchange, routing_key) pair
// observed by the publish histogram and counter through
// [promutil.OpaqueLabelValue] so per-tenant or per-resource segments
// do not blow up Prometheus cardinality.
//
// This is the v2 default — services no longer need to call this
// option explicitly. Pair with [WithRawRouteLabels] to revert.
func WithOpaqueRouteLabels() MetricsOption {
	return func(c *metricsConfig) {
		c.labelRoute = opaqueRouteLabel
	}
}

// WithRawRouteLabels reverts to v1-style raw exchange / routing-key
// labels. Use ONLY when the deployment has audited every publisher
// and confirmed route segments are static / low-cardinality.
func WithRawRouteLabels() MetricsOption {
	return func(c *metricsConfig) {
		c.labelRoute = passthroughRouteLabel
	}
}

func opaqueRouteLabel(exchange, routingKey string) (string, string) {
	return promutil.OpaqueLabelValue("exchange", exchange),
		promutil.OpaqueLabelValue("routingkey", routingKey)
}

// NewMetrics creates and registers NATS metrics. Pass [WithRegisterer]
// to use a non-default registry. Route labels default to the bounded /
// opaque form (v2 cardinality-safe default); pass [WithRawRouteLabels]
// only when the routing topology is audited and known to be low
// cardinality. Repeated calls reuse already-registered collectors on
// the same registry.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{
		registerer: prometheus.DefaultRegisterer,
		labelRoute: opaqueRouteLabel,
	}
	for _, opt := range opts {
		if opt == nil {
			panic("natsbackend: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer
	m := &Metrics{
		published: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nats",
			Name:      "published_total",
			Help:      "Total NATS JetStream publish attempts by exchange, routing key, and outcome.",
		}, []string{"exchange", "routing_key", "outcome"}),
		publishDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "nats",
			Name:      "publish_duration_seconds",
			Help:      "NATS JetStream publish duration by exchange, routing key, and outcome.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"exchange", "routing_key", "outcome"}),
		consumed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "nats",
			Name:      "consumed_total",
			Help:      "Total NATS JetStream deliveries handled by stream, durable, and final outcome.",
		}, []string{"stream", "durable", "outcome"}),
		handlerDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "nats",
			Name:      "handler_duration_seconds",
			Help:      "NATS JetStream handler execution duration by stream, durable, and handler outcome.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"stream", "durable", "outcome"}),
	}

	m.published = promutil.MustRegisterOrGet(reg, m.published)
	m.publishDuration = promutil.MustRegisterOrGet(reg, m.publishDuration)
	m.consumed = promutil.MustRegisterOrGet(reg, m.consumed)
	m.handlerDuration = promutil.MustRegisterOrGet(reg, m.handlerDuration)
	m.labelRoute = cfg.labelRoute
	return m
}

func (m *Metrics) observePublish(exchange, routingKey, outcome string, started time.Time) {
	if m == nil {
		return
	}
	ex, rk := m.routeLabel(exchange, routingKey)
	m.published.WithLabelValues(ex, rk, outcome).Inc()
	m.publishDuration.WithLabelValues(ex, rk, outcome).Observe(time.Since(started).Seconds())
}

func (m *Metrics) routeLabel(exchange, routingKey string) (string, string) {
	if m == nil || m.labelRoute == nil {
		return exchange, routingKey
	}
	return m.labelRoute(exchange, routingKey)
}

func (m *Metrics) observeConsumed(stream, durable, outcome string) {
	if m == nil {
		return
	}
	m.consumed.WithLabelValues(stream, durable, outcome).Inc()
}

func (m *Metrics) observeHandler(stream, durable, outcome string, started time.Time) {
	if m == nil {
		return
	}
	m.handlerDuration.WithLabelValues(stream, durable, outcome).Observe(time.Since(started).Seconds())
}
