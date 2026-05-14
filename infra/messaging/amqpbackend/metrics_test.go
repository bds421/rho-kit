package amqpbackend

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func TestAMQPMetrics_ReusesCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m1 := NewMetrics(WithRegisterer(reg))
	m2 := NewMetrics(WithRegisterer(reg))

	assert.Same(t, m1.published, m2.published)
	assert.Same(t, m1.publishDuration, m2.publishDuration)
	assert.Same(t, m1.consumed, m2.consumed)
	assert.Same(t, m1.handlerDuration, m2.handlerDuration)
	assert.Same(t, m1.connectionUp, m2.connectionUp)
	assert.Same(t, m1.reconnectAttempts, m2.reconnectAttempts)
	assert.Same(t, m1.consecutiveReconnectFailures, m2.consecutiveReconnectFailures)
}

// TestAMQPMetrics_ConnectionUp_FlipsOnUpAndDown pins the gauge semantics —
// successful dial sets the gauge to 1, startReconnect sets it to 0. The
// /healthz alert rules on this gauge directly so the transitions must be
// observable from a Prometheus scrape, not derived from logs.
func TestAMQPMetrics_ConnectionUp_FlipsOnUpAndDown(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))

	m.observeConnectionUp("primary", true)
	assert.Equal(t, 1.0, testutil.ToFloat64(m.connectionUp.WithLabelValues("primary")))

	m.observeConnectionUp("primary", false)
	assert.Equal(t, 0.0, testutil.ToFloat64(m.connectionUp.WithLabelValues("primary")))
}

// TestAMQPMetrics_ReconnectAttempts_TracksOutcomes confirms the counter
// splits by outcome and that the consecutive-failures gauge resets on
// the first success after a streak of failures (so dashboards do not
// alert forever after recovery).
func TestAMQPMetrics_ReconnectAttempts_TracksOutcomes(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))

	m.observeReconnectAttempt("primary", amqpReconnectOutcomeFailed)
	m.observeReconnectAttempt("primary", amqpReconnectOutcomeFailed)
	m.observeReconnectAttempt("primary", amqpReconnectOutcomeFailed)

	assert.Equal(t, 3.0, testutil.ToFloat64(m.reconnectAttempts.WithLabelValues("primary", amqpReconnectOutcomeFailed)))
	assert.Equal(t, 3.0, testutil.ToFloat64(m.consecutiveReconnectFailures.WithLabelValues("primary")))

	m.observeReconnectAttempt("primary", amqpReconnectOutcomeSuccess)
	assert.Equal(t, 1.0, testutil.ToFloat64(m.reconnectAttempts.WithLabelValues("primary", amqpReconnectOutcomeSuccess)))
	assert.Equal(t, 0.0, testutil.ToFloat64(m.consecutiveReconnectFailures.WithLabelValues("primary")))
}

// TestAMQPMetrics_ReconnectAttempts_EmptyBrokerFallsBack proves the
// bounded-label safety net: a connection that observes metrics without
// an explicit broker label resolves to "default" so cardinality stays
// predictable.
func TestAMQPMetrics_ReconnectAttempts_EmptyBrokerFallsBack(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))

	m.observeReconnectAttempt("", amqpReconnectOutcomeFailed)
	assert.Equal(t, 1.0, testutil.ToFloat64(m.reconnectAttempts.WithLabelValues("default", amqpReconnectOutcomeFailed)))
}

// TestAMQPMetrics_NilSafe documents the no-op contract: methods on a nil
// *Metrics never panic, so a Connection without WithConnectionMetrics is
// free of nil-deref hazards.
func TestAMQPMetrics_NilSafe(t *testing.T) {
	var m *Metrics
	require.NotPanics(t, func() {
		m.observeConnectionUp("primary", true)
		m.observeReconnectAttempt("primary", amqpReconnectOutcomeFailed)
	})
}

// TestWithConnectionMetrics_NilPanics — fail fast at construction so a
// misconfigured DialOption surfaces at Connect() rather than at first
// metric observation.
func TestWithConnectionMetrics_NilPanics(t *testing.T) {
	assert.Panics(t, func() { WithConnectionMetrics(nil, "primary") })
}

func TestAMQPMetrics_Contract(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))
	now := time.Now()

	metrics.observePublish("events", "order.created", amqpPublishOutcomeSuccess, now)
	metrics.observeConsumed("orders.created", amqpConsumeOutcomeAcked)
	metrics.observeHandler("orders.created", amqpHandlerOutcomeSuccess, now)

	assertMetricLabels(t, reg, "amqp_published_total", []string{"exchange", "outcome", "routing_key"})
	assertMetricLabels(t, reg, "amqp_publish_duration_seconds", []string{"exchange", "outcome", "routing_key"})
	assertMetricLabels(t, reg, "amqp_consumed_total", []string{"outcome", "queue"})
	assertMetricLabels(t, reg, "amqp_handler_duration_seconds", []string{"outcome", "queue"})
}

func TestPublisherMetrics_RecordTooLargePublish(t *testing.T) {
	metrics := NewMetrics(WithRegisterer(prometheus.NewRegistry()))
	pub := NewPublisher(noopConnector{}, discardLogger(),
		WithMaxMessageBytes(4),
		WithPublisherMetrics(metrics),
	)

	err := pub.PublishRaw(context.Background(), "events", "large.event", []byte("too-large"), "msg-1")

	require.Error(t, err)
	assert.ErrorIs(t, err, messaging.ErrMessageTooLarge)
	assertPublish(t, metrics, "events", "large.event", amqpPublishOutcomeTooLarge, 1)
}

func TestConsumerMetrics_RecordDeliveryOutcomes(t *testing.T) {
	metrics := NewMetrics(WithRegisterer(prometheus.NewRegistry()))
	c := newTestConsumer(nil, ConsumerHooks{})
	c.metrics = metrics
	msg, err := messaging.NewMessage("test.event", "payload")
	require.NoError(t, err)
	binding := messaging.Binding{BindingSpec: messaging.BindingSpec{Queue: "test-queue"}}

	t.Run("acked", func(t *testing.T) {
		ack := &fakeAcknowledger{}
		c.handleDelivery(context.Background(), makeAMQPDelivery(ack, msg),
			func(context.Context, messaging.Delivery) error { return nil },
			binding,
		)
		assert.True(t, ack.acked)
		assertConsume(t, metrics, "test-queue", amqpConsumeOutcomeAcked, 1)
	})

	t.Run("decode_error", func(t *testing.T) {
		ack := &fakeAcknowledger{}
		c.handleDelivery(context.Background(), invalidJSONDelivery(ack),
			func(context.Context, messaging.Delivery) error { return nil },
			binding,
		)
		assert.True(t, ack.acked)
		assertConsume(t, metrics, "test-queue", amqpConsumeOutcomeDecodeError, 1)
	})
}

func TestConsumerMetrics_RecordFailureOutcomes(t *testing.T) {
	metrics := NewMetrics(WithRegisterer(prometheus.NewRegistry()))
	msg, err := messaging.NewMessage("test.event", "payload")
	require.NoError(t, err)

	t.Run("retry", func(t *testing.T) {
		c := newTestConsumer(nil, ConsumerHooks{})
		c.metrics = metrics
		ack := &fakeAcknowledger{}
		binding := messaging.Binding{BindingSpec: messaging.BindingSpec{
			Queue:      "retry-queue",
			RoutingKey: "test.event",
			Retry:      &messaging.RetryPolicy{MaxRetries: 3},
		}}

		c.handleFailure(context.Background(), makeAMQPDelivery(ack, msg), msg, binding, errors.New("transient"))

		assert.True(t, ack.nacked)
		assertConsume(t, metrics, "retry-queue", amqpConsumeOutcomeRetry, 1)
	})

	t.Run("dead_lettered", func(t *testing.T) {
		c := newTestConsumer(&fakeDeadLetterPublisher{}, ConsumerHooks{})
		c.metrics = metrics
		ack := &fakeAcknowledger{}
		binding := messaging.Binding{
			BindingSpec: messaging.BindingSpec{
				Queue:      "dead-queue",
				RoutingKey: "test.event",
				Retry:      &messaging.RetryPolicy{MaxRetries: 3},
			},
			DeadExchange: "events.dead",
		}
		delivery := makeAMQPDelivery(ack, msg)
		delivery.Headers = deadLetterHeaders("dead-queue", 3)

		c.handleFailure(context.Background(), delivery, msg, binding, errors.New("too many retries"))

		assert.True(t, ack.acked)
		assertConsume(t, metrics, "dead-queue", amqpConsumeOutcomeDeadLettered, 1)
	})
}

func TestAMQPMetricsOptions_PanicOnNilMetrics(t *testing.T) {
	assert.Panics(t, func() { WithPublisherMetrics(nil) })
	assert.Panics(t, func() { WithConsumerMetrics(nil) })
}

func invalidJSONDelivery(ack *fakeAcknowledger) amqp.Delivery {
	return amqp.Delivery{
		Acknowledger: ack,
		Body:         []byte(`bad json`),
	}
}

func deadLetterHeaders(queue string, count int64) amqp.Table {
	return amqp.Table{
		"x-death": []any{
			amqp.Table{
				"queue":  queue,
				"reason": "rejected",
				"count":  count,
			},
		},
	}
}

func assertPublish(t *testing.T, m *Metrics, exchange, routingKey, outcome string, want float64) {
	t.Helper()
	got := testutil.ToFloat64(m.published.WithLabelValues(exchange, routingKey, outcome))
	if got != want {
		t.Fatalf("publish %s/%s/%s = %v, want %v", exchange, routingKey, outcome, got, want)
	}
}

func assertConsume(t *testing.T, m *Metrics, queue, outcome string, want float64) {
	t.Helper()
	got := testutil.ToFloat64(m.consumed.WithLabelValues(queue, outcome))
	if got != want {
		t.Fatalf("consume %s/%s = %v, want %v", queue, outcome, got, want)
	}
}

func assertMetricLabels(t *testing.T, reg *prometheus.Registry, family string, want []string) {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range families {
		if mf.GetName() != family {
			continue
		}
		require.NotEmpty(t, mf.GetMetric(), "metric family %s has no samples", family)
		labels := make([]string, 0, len(mf.GetMetric()[0].GetLabel()))
		for _, label := range mf.GetMetric()[0].GetLabel() {
			labels = append(labels, label.GetName())
		}
		slices.Sort(labels)
		slices.Sort(want)
		assert.Equal(t, want, labels, "labels for %s", family)
		return
	}
	t.Fatalf("metric family %s not found", family)
}
