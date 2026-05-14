package natsbackend

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

func TestNATSMetrics_ReusesCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m1 := NewMetrics(WithRegisterer(reg))
	m2 := NewMetrics(WithRegisterer(reg))

	assert.Same(t, m1.published, m2.published)
	assert.Same(t, m1.publishDuration, m2.publishDuration)
	assert.Same(t, m1.consumed, m2.consumed)
	assert.Same(t, m1.handlerDuration, m2.handlerDuration)
}

func TestNATSMetrics_Contract(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))
	now := time.Now()

	metrics.observePublish("events", "order.created", natsPublishOutcomeSuccess, now)
	metrics.observeConsumed("EVENTS", "orders", natsConsumeOutcomeAcked)
	metrics.observeHandler("EVENTS", "orders", natsHandlerOutcomeSuccess, now)

	assertMetricLabels(t, reg, "nats_published_total", []string{"exchange", "outcome", "routing_key"})
	assertMetricLabels(t, reg, "nats_publish_duration_seconds", []string{"exchange", "outcome", "routing_key"})
	assertMetricLabels(t, reg, "nats_consumed_total", []string{"durable", "outcome", "stream"})
	assertMetricLabels(t, reg, "nats_handler_duration_seconds", []string{"durable", "outcome", "stream"})
}

func TestNATSConsumerMetrics_RecordDispatchOutcomes(t *testing.T) {
	metrics := NewMetrics(WithRegisterer(prometheus.NewRegistry()))
	c := &Consumer{
		cfg:     ConsumerConfig{Stream: "EVENTS", Durable: "orders"},
		logger:  slog.Default(),
		metrics: metrics,
	}
	msg, err := messaging.NewMessage("order.created", map[string]string{"id": "42"})
	require.NoError(t, err)
	body := mustJSON(t, msg)

	t.Run("acked", func(t *testing.T) {
		jm := &fakeNATSMsg{subject: "events.order%2Ecreated", data: body}
		c.dispatch(context.Background(), jm, func(context.Context, messaging.Delivery) error { return nil })

		assert.True(t, jm.acked)
		assertNATSConsume(t, metrics, "EVENTS", "orders", natsConsumeOutcomeAcked, 1)
	})

	t.Run("retry", func(t *testing.T) {
		jm := &fakeNATSMsg{subject: "events.order%2Ecreated", data: body}
		c.dispatch(context.Background(), jm, func(context.Context, messaging.Delivery) error {
			return errors.New("transient")
		})

		assert.True(t, jm.nacked)
		assertNATSConsume(t, metrics, "EVENTS", "orders", natsConsumeOutcomeRetry, 1)
	})

	t.Run("decode_error", func(t *testing.T) {
		jm := &fakeNATSMsg{subject: "events.order%2Ecreated", data: []byte("bad json")}
		c.dispatch(context.Background(), jm, func(context.Context, messaging.Delivery) error { return nil })

		assert.True(t, jm.termed)
		assertNATSConsume(t, metrics, "EVENTS", "orders", natsConsumeOutcomeDecodeError, 1)
	})

	t.Run("handler_panic", func(t *testing.T) {
		jm := &fakeNATSMsg{subject: "events.order%2Ecreated", data: body}
		c.dispatch(context.Background(), jm, func(context.Context, messaging.Delivery) error {
			panic("boom")
		})

		assert.True(t, jm.termed)
		assertNATSConsume(t, metrics, "EVENTS", "orders", natsConsumeOutcomeHandlerPanic, 1)
	})
}

func TestNATSMetricsOptions_PanicOnNilMetrics(t *testing.T) {
	assert.Panics(t, func() { WithPublisherMetrics(nil) })
	assert.Panics(t, func() { WithConsumerMetrics(nil) })
}

type fakeNATSMsg struct {
	subject string
	data    []byte
	headers nats.Header

	acked  bool
	nacked bool
	termed bool

	ackErr  error
	nakErr  error
	termErr error
}

func (f *fakeNATSMsg) Metadata() (*jetstream.MsgMetadata, error) {
	return &jetstream.MsgMetadata{}, nil
}

func (f *fakeNATSMsg) Data() []byte { return f.data }

func (f *fakeNATSMsg) Headers() nats.Header { return f.headers }

func (f *fakeNATSMsg) Subject() string { return f.subject }

func (f *fakeNATSMsg) Reply() string { return "" }

func (f *fakeNATSMsg) Ack() error {
	f.acked = true
	return f.ackErr
}

func (f *fakeNATSMsg) DoubleAck(context.Context) error {
	f.acked = true
	return f.ackErr
}

func (f *fakeNATSMsg) Nak() error {
	f.nacked = true
	return f.nakErr
}

func (f *fakeNATSMsg) NakWithDelay(time.Duration) error {
	f.nacked = true
	return f.nakErr
}

func (f *fakeNATSMsg) InProgress() error { return nil }

func (f *fakeNATSMsg) Term() error {
	f.termed = true
	return f.termErr
}

func (f *fakeNATSMsg) TermWithReason(string) error {
	f.termed = true
	return f.termErr
}

func assertNATSConsume(t *testing.T, m *Metrics, stream, durable, outcome string, want float64) {
	t.Helper()
	got := testutil.ToFloat64(m.consumed.WithLabelValues(stream, durable, outcome))
	if got != want {
		t.Fatalf("consume %s/%s/%s = %v, want %v", stream, durable, outcome, got, want)
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
		require.NotEmpty(t, mf.GetMetric(), "%s has no metrics", family)
		labels := mf.GetMetric()[0].GetLabel()
		got := make([]string, 0, len(labels))
		for _, label := range labels {
			got = append(got, label.GetName())
		}
		assert.Equal(t, want, got, family)
		return
	}
	t.Fatalf("metric family %s not found", family)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	require.NoError(t, err)
	return out
}

// TestNATSMetrics_OpaqueRouteLabelsBoundsCardinality pins M-008 for
// the NATS publish-metric path. WithOpaqueRouteLabels must hash the
// exchange + routing-key labels so tenant-bearing routes do not blow
// up Prometheus series count.
func TestNATSMetrics_OpaqueRouteLabelsBoundsCardinality(t *testing.T) {
	_ = testutil.CollectAndCount // ensure testutil retains a use
	rawReg := prometheus.NewRegistry()
	opaqueReg := prometheus.NewRegistry()
	raw := NewMetrics(WithRegisterer(rawReg))
	opaque := NewMetrics(WithRegisterer(opaqueReg), WithOpaqueRouteLabels())

	const tenantyKey = "orders.tenant-123-secret-id.created"
	raw.observePublish("orders", tenantyKey, "success", time.Now())
	opaque.observePublish("orders", tenantyKey, "success", time.Now())

	rawFams, err := rawReg.Gather()
	require.NoError(t, err)
	require.True(t, natsContainsLabel(rawFams, "nats_published_total", "routing_key", tenantyKey),
		"default NATS Metrics must record the raw routing key")

	opaqueFams, err := opaqueReg.Gather()
	require.NoError(t, err)
	require.False(t, natsContainsLabel(opaqueFams, "nats_published_total", "routing_key", tenantyKey),
		"WithOpaqueRouteLabels must drop the raw tenanty routing key")
	require.True(t, natsHasLabelPrefix(opaqueFams, "nats_published_total", "routing_key", "routingkey"),
		"opaque label keeps the static 'routingkey' visible prefix for dashboards")
}

func natsContainsLabel(fams []*dto.MetricFamily, family, name, value string) bool {
	for _, mf := range fams {
		if mf.GetName() != family {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == name && lp.GetValue() == value {
					return true
				}
			}
		}
	}
	return false
}

func natsHasLabelPrefix(fams []*dto.MetricFamily, family, name, prefix string) bool {
	for _, mf := range fams {
		if mf.GetName() != family {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == name && strings.HasPrefix(lp.GetValue(), prefix) {
					return true
				}
			}
		}
	}
	return false
}
