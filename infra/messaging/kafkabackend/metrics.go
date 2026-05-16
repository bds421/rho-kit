package kafkabackend

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

const (
	kafkaPublishOutcomeSuccess        = "success"
	kafkaPublishOutcomeFailed         = "failed"
	kafkaPublishOutcomeInvalidMessage = "invalid_message"
	kafkaPublishOutcomeTooLarge       = "too_large"

	kafkaConsumeOutcomeAcked         = "acked"
	kafkaConsumeOutcomeCommitFailed  = "commit_failed"
	kafkaConsumeOutcomeRetry         = "retry"
	kafkaConsumeOutcomePermanent     = "permanent"
	kafkaConsumeOutcomeDecodeError   = "decode_error"
	kafkaConsumeOutcomeValidateError = "validate_error"
	kafkaConsumeOutcomeHandlerPanic  = "handler_panic"
	kafkaConsumeOutcomeFetchError    = "fetch_error"

	kafkaHandlerOutcomeSuccess        = "success"
	kafkaHandlerOutcomeError          = "error"
	kafkaHandlerOutcomePanic          = "panic"
	kafkaHandlerOutcomeDecodeError    = "decode_error"
	kafkaHandlerOutcomeValidateError  = "validate_error"
)

// Metrics holds Prometheus collectors for the Kafka publisher and
// subscriber.
//
// Label discipline mirrors the AMQP/NATS backends: topology-level
// dimensions only. Topic and consumer-group routinely have static low
// cardinality, BUT operators with high-cardinality groups (per-tenant
// or per-user consumer naming) can still blow up Prometheus. Both
// publish-side route labels (topic, routing_key) AND consume-side
// labels (topic, group) default to the bounded / opaque form in v2.
// Pass [WithRawRouteLabels] / [WithRawConsumeLabels] to opt out only
// after auditing the topology.
type Metrics struct {
	published       *prometheus.CounterVec
	publishDuration *prometheus.HistogramVec
	consumed        *prometheus.CounterVec
	handlerDuration *prometheus.HistogramVec

	labelRoute   routeLabelFunc
	labelConsume consumeLabelFunc
}

type routeLabelFunc func(exchange, routingKey string) (string, string)
type consumeLabelFunc func(topic, group string) (string, string)

func passthroughRouteLabel(exchange, routingKey string) (string, string) {
	return exchange, routingKey
}

func opaqueRouteLabel(exchange, routingKey string) (string, string) {
	return promutil.OpaqueLabelValue("topic", exchange),
		promutil.OpaqueLabelValue("routingkey", routingKey)
}

func passthroughConsumeLabel(topic, group string) (string, string) {
	return topic, group
}

func opaqueConsumeLabel(topic, group string) (string, string) {
	return promutil.OpaqueLabelValue("topic", topic),
		promutil.OpaqueLabelValue("group", group)
}

// MetricsOption configures the Kafka metric constructor. The
// canonical shape (`NewMetrics(opts ...MetricsOption)`) matches every
// other rho-kit metric constructor.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer   prometheus.Registerer
	labelRoute   routeLabelFunc
	labelConsume consumeLabelFunc
}

// WithRegisterer pins the Prometheus registerer used for Kafka
// metrics. When unset, [prometheus.DefaultRegisterer] is used.
// Passing nil panics so a miswired "metrics enabled, registerer not
// supplied" caller surfaces at startup rather than going to the
// global default.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("kafkabackend: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// WithOpaqueRouteLabels (the default) passes every (topic,
// routing_key) pair through [promutil.OpaqueLabelValue] so per-tenant
// segments cannot blow up Prometheus cardinality.
func WithOpaqueRouteLabels() MetricsOption {
	return func(c *metricsConfig) { c.labelRoute = opaqueRouteLabel }
}

// WithRawRouteLabels reverts to raw topic / routing-key labels. Use
// ONLY when the deployment has audited every publisher.
func WithRawRouteLabels() MetricsOption {
	return func(c *metricsConfig) { c.labelRoute = passthroughRouteLabel }
}

// WithOpaqueConsumeLabels (the default) passes every (topic, group)
// pair on the consume side through [promutil.OpaqueLabelValue] so
// per-tenant consumer-group naming cannot blow up Prometheus
// cardinality. Wave 140 made this the default for both NATS and
// Kafka; AMQP's consume labels stay opaque via the same mechanism.
func WithOpaqueConsumeLabels() MetricsOption {
	return func(c *metricsConfig) { c.labelConsume = opaqueConsumeLabel }
}

// WithRawConsumeLabels reverts to raw topic / consumer-group labels.
// Use ONLY when the deployment has audited consumer-group naming and
// confirmed low cardinality.
func WithRawConsumeLabels() MetricsOption {
	return func(c *metricsConfig) { c.labelConsume = passthroughConsumeLabel }
}

// NewMetrics constructs the Kafka metric set. Pass [WithRegisterer] to
// use a non-default registry. Repeated calls reuse already-registered
// collectors on the same registry.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{
		registerer:   prometheus.DefaultRegisterer,
		labelRoute:   opaqueRouteLabel,
		labelConsume: opaqueConsumeLabel,
	}
	for _, opt := range opts {
		if opt == nil {
			panic("kafkabackend: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	reg := cfg.registerer
	m := &Metrics{
		published: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "kafka",
			Name:      "published_total",
			Help:      "Total Kafka publish attempts by topic, routing key, and outcome.",
		}, []string{"topic", "routing_key", "outcome"}),
		publishDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "kafka",
			Name:      "publish_duration_seconds",
			Help:      "Kafka publish duration by topic, routing key, and outcome.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"topic", "routing_key", "outcome"}),
		consumed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "kafka",
			Name:      "consumed_total",
			Help:      "Total Kafka deliveries handled by topic, consumer group, and final outcome.",
		}, []string{"topic", "group", "outcome"}),
		handlerDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "kafka",
			Name:      "handler_duration_seconds",
			Help:      "Kafka handler execution duration by topic, consumer group, and handler outcome.",
			Buckets:   prometheus.DefBuckets,
		}, []string{"topic", "group", "outcome"}),
	}
	m.published = promutil.MustRegisterOrGet(reg, m.published)
	m.publishDuration = promutil.MustRegisterOrGet(reg, m.publishDuration)
	m.consumed = promutil.MustRegisterOrGet(reg, m.consumed)
	m.handlerDuration = promutil.MustRegisterOrGet(reg, m.handlerDuration)
	m.labelRoute = cfg.labelRoute
	m.labelConsume = cfg.labelConsume
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

func (m *Metrics) consumeLabel(topic, group string) (string, string) {
	if m == nil || m.labelConsume == nil {
		return topic, group
	}
	return m.labelConsume(topic, group)
}

func (m *Metrics) observeConsumed(topic, group, outcome string) {
	if m == nil {
		return
	}
	t, g := m.consumeLabel(topic, group)
	m.consumed.WithLabelValues(t, g, outcome).Inc()
}

func (m *Metrics) observeHandler(topic, group, outcome string, started time.Time) {
	if m == nil {
		return
	}
	t, g := m.consumeLabel(topic, group)
	m.handlerDuration.WithLabelValues(t, g, outcome).Observe(time.Since(started).Seconds())
}
