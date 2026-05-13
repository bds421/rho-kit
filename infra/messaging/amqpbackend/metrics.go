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
	// explicit broker name via WithReconnectMetrics for services that
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

	connectionUp                  *prometheus.GaugeVec
	reconnectAttempts             *prometheus.CounterVec
	consecutiveReconnectFailures  *prometheus.GaugeVec
}

// NewMetrics creates and registers AMQP metrics with the given registerer.
// If reg is nil, prometheus.DefaultRegisterer is used. Repeated calls reuse
// already-registered collectors on the same registry.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}
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
	return m
}

func (m *Metrics) observePublish(exchange, routingKey, outcome string, started time.Time) {
	if m == nil {
		return
	}
	m.published.WithLabelValues(exchange, routingKey, outcome).Inc()
	m.publishDuration.WithLabelValues(exchange, routingKey, outcome).Observe(time.Since(started).Seconds())
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
