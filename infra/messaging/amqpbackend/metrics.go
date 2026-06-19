package amqpbackend

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

const (
	amqpPublishOutcomeSuccess        = "success"
	amqpPublishOutcomeFailed         = "failed"
	amqpPublishOutcomeInvalidMessage = "invalid_message"
	amqpPublishOutcomeTooLarge       = "too_large"
	amqpPublishOutcomeUnroutable     = "unroutable"

	amqpConsumeOutcomeAcked            = "acked"
	amqpConsumeOutcomeAckFailed        = "ack_failed"
	amqpConsumeOutcomeDecodeError      = "decode_error"
	amqpConsumeOutcomeValidateError    = "validate_error"
	amqpConsumeOutcomeRetry            = "retry"
	amqpConsumeOutcomeDeadLettered     = "dead_lettered"
	amqpConsumeOutcomeDiscarded        = "discarded"
	amqpConsumeOutcomeForceDiscarded   = "force_discarded"
	amqpConsumeOutcomeDLQPublishFailed = "dlq_publish_failed"

	amqpHandlerOutcomeSuccess = "success"
	amqpHandlerOutcomeError   = "error"

	amqpReconnectOutcomeSuccess = "success"
	amqpReconnectOutcomeFailed  = "failed"

	// defaultBrokerLabel is used when a connection observes reconnect
	// metrics without an explicit broker name. The label value is bounded
	// and static so Prometheus cardinality stays predictable; pass an
	// explicit broker name via WithConnectionMetrics for services that
	// dial more than one broker.
	defaultBrokerLabel = "default"
)

// Metrics holds Prometheus collectors for direct AMQP publishing and consuming.
//
// Labels are intentionally restricted to topology-level dimensions:
// exchange, routing_key, queue, and outcome. Do not encode tenant IDs,
// request IDs, user IDs, or payload-derived values into AMQP topology names
// when these metrics are enabled.
//
// The connection lifecycle gauges (connection_up, consecutive_reconnect_failures)
// and reconnect_attempts counter are labelled by broker so a single registry
// can serve multiple AMQP connections (publish-only + consume-only, primary +
// DR) without colliding samples.
type Metrics struct {
	published       *prometheus.CounterVec
	publishDuration *prometheus.HistogramVec
	consumed        *prometheus.CounterVec
	handlerDuration *prometheus.HistogramVec

	connectionUp                 *prometheus.GaugeVec
	reconnectAttempts            *prometheus.CounterVec
	consecutiveReconnectFailures *prometheus.GaugeVec

	// labelRoute maps a raw (exchange, routingKey) pair to the label
	// values that will reach Prometheus. The v2 default is the
	// cardinality-safe opaque (hashed) form ([opaqueRouteLabel]);
	// services that have audited their topology can opt back into
	// raw labels with [WithRawRouteLabels].
	labelRoute routeLabelFunc
}

type routeLabelFunc func(exchange, routingKey string) (string, string)

func passthroughRouteLabel(exchange, routingKey string) (string, string) {
	return exchange, routingKey
}

// MetricsOption configures the AMQP metric constructor. Standardised
// across the kit so every package exposes `NewMetrics(opts ...MetricsOption)`.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
	labelRoute routeLabelFunc
}

// WithRegisterer pins the Prometheus registerer used for AMQP
// metrics. When unset, [prometheus.DefaultRegisterer] is used. Passing
// nil panics so a miswired "metrics enabled, registerer not supplied"
// caller surfaces at startup rather than going to the global default.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("amqpbackend: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// WithOpaqueRouteLabels passes every (exchange, routing_key) pair
// observed by the publish histogram and counter through
// [promutil.OpaqueLabelValue]. The visible label keeps a static
// prefix (`exchange` / `routingkey`), while the variable suffix is a
// deterministic SHA-256 truncated hash so per-tenant or per-resource
// route segments do not blow up Prometheus cardinality.
//
// This is the v2 default — services no longer need to call this
// option explicitly. It remains exported so dashboards that need an
// explicit signal at construction time can keep the wiring obvious.
// Pair with [WithRawRouteLabels] to revert to v1-style raw labels for
// dashboards that explicitly want them.
func WithOpaqueRouteLabels() MetricsOption {
	return func(c *metricsConfig) {
		c.labelRoute = opaqueRouteLabel
	}
}

// WithRawRouteLabels reverts to v1-style raw exchange / routing-key
// labels. Use ONLY when the deployment has audited every publisher
// and confirmed route segments are static / low-cardinality — a single
// tenant ID embedded in a routing key under raw labels turns
// `amqp_published_total` into a per-tenant series and breaks
// Prometheus.
func WithRawRouteLabels() MetricsOption {
	return func(c *metricsConfig) {
		c.labelRoute = passthroughRouteLabel
	}
}

func opaqueRouteLabel(exchange, routingKey string) (string, string) {
	return promutil.OpaqueLabelValue("exchange", exchange),
		promutil.OpaqueLabelValue("routingkey", routingKey)
}

// NewMetrics creates and registers AMQP metrics. Pass [WithRegisterer]
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
			panic("amqpbackend: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer
	m := &Metrics{
		published: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "amqp",
			Name:      "published_total",
			Help:      "Total AMQP publish attempts by exchange, routing key, and outcome.",
		}, []string{"exchange", "routing_key", "outcome"}),
		publishDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "amqp",
			Name:      "publish_duration_seconds",
			Help:      "AMQP publish duration by exchange, routing key, and outcome.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"exchange", "routing_key", "outcome"}),
		consumed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "amqp",
			Name:      "consumed_total",
			Help:      "Total AMQP deliveries handled by queue and final outcome.",
		}, []string{"queue", "outcome"}),
		handlerDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "amqp",
			Name:      "handler_duration_seconds",
			Help:      "AMQP handler execution duration by queue and handler outcome.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"queue", "outcome"}),
		connectionUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "amqp",
			Name:      "connection_up",
			Help:      "1 when the AMQP connection to the labelled broker is established, 0 while a reconnect loop is running.",
		}, []string{"broker"}),
		reconnectAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "amqp",
			Name:      "reconnect_attempts_total",
			Help:      "Total AMQP reconnect attempts by broker and outcome (success|failed).",
		}, []string{"broker", "outcome"}),
		consecutiveReconnectFailures: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: "amqp",
			Name:      "consecutive_reconnect_failures",
			Help:      "Consecutive failed reconnect attempts since the last successful AMQP connect; resets to 0 on success.",
		}, []string{"broker"}),
	}

	m.published = promutil.MustRegisterOrGet(reg, m.published)
	m.publishDuration = promutil.MustRegisterOrGet(reg, m.publishDuration)
	m.consumed = promutil.MustRegisterOrGet(reg, m.consumed)
	m.handlerDuration = promutil.MustRegisterOrGet(reg, m.handlerDuration)
	m.connectionUp = promutil.MustRegisterOrGet(reg, m.connectionUp)
	m.reconnectAttempts = promutil.MustRegisterOrGet(reg, m.reconnectAttempts)
	m.consecutiveReconnectFailures = promutil.MustRegisterOrGet(reg, m.consecutiveReconnectFailures)
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

func (m *Metrics) observeConsumed(queue, outcome string) {
	if m == nil {
		return
	}
	m.consumed.WithLabelValues(queue, outcome).Inc()
}

func (m *Metrics) observeHandler(queue, outcome string, started time.Time) {
	if m == nil {
		return
	}
	m.handlerDuration.WithLabelValues(queue, outcome).Observe(time.Since(started).Seconds())
}

// observeConnectionUp sets the connection_up gauge for broker. Pass 1 for
// "connected" and 0 for "reconnect loop running / disconnected".
func (m *Metrics) observeConnectionUp(broker string, up bool) {
	if m == nil {
		return
	}
	if broker == "" {
		broker = defaultBrokerLabel
	}
	v := 0.0
	if up {
		v = 1.0
	}
	m.connectionUp.WithLabelValues(broker).Set(v)
}

// observeReconnectAttempt increments the reconnect_attempts counter with the
// given outcome (success|failed) and updates consecutive_reconnect_failures:
// success resets the gauge to zero, failure increments it.
func (m *Metrics) observeReconnectAttempt(broker, outcome string) {
	if m == nil {
		return
	}
	if broker == "" {
		broker = defaultBrokerLabel
	}
	m.reconnectAttempts.WithLabelValues(broker, outcome).Inc()
	switch outcome {
	case amqpReconnectOutcomeSuccess:
		m.consecutiveReconnectFailures.WithLabelValues(broker).Set(0)
	case amqpReconnectOutcomeFailed:
		m.consecutiveReconnectFailures.WithLabelValues(broker).Inc()
	}
}
