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
	amqpConsumeOutcomeRetry            = "retry"
	amqpConsumeOutcomeDeadLettered     = "dead_lettered"
	amqpConsumeOutcomeDiscarded        = "discarded"
	amqpConsumeOutcomeForceDiscarded   = "force_discarded"
	amqpConsumeOutcomeDLQPublishFailed = "dlq_publish_failed"

	amqpHandlerOutcomeSuccess = "success"
	amqpHandlerOutcomeError   = "error"
)

// Metrics holds Prometheus collectors for direct AMQP publishing and consuming.
//
// Labels are intentionally restricted to topology-level dimensions:
// exchange, routing_key, queue, and outcome. Do not encode tenant IDs,
// request IDs, user IDs, or payload-derived values into AMQP topology names
// when these metrics are enabled.
type Metrics struct {
	published       *prometheus.CounterVec
	publishDuration *prometheus.HistogramVec
	consumed        *prometheus.CounterVec
	handlerDuration *prometheus.HistogramVec
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
	}

	m.published = promutil.MustRegisterOrGet(reg, m.published)
	m.publishDuration = promutil.MustRegisterOrGet(reg, m.publishDuration)
	m.consumed = promutil.MustRegisterOrGet(reg, m.consumed)
	m.handlerDuration = promutil.MustRegisterOrGet(reg, m.handlerDuration)
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
