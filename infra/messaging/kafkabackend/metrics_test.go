package kafkabackend

import (
	"sort"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKafkaMetrics_ReusesCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m1 := NewMetrics(WithRegisterer(reg))
	m2 := NewMetrics(WithRegisterer(reg))

	assert.Same(t, m1.published, m2.published)
	assert.Same(t, m1.publishDuration, m2.publishDuration)
	assert.Same(t, m1.consumed, m2.consumed)
	assert.Same(t, m1.handlerDuration, m2.handlerDuration)
}

func TestKafkaMetrics_LabelShape(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))
	now := time.Now()

	metrics.observePublish("events", "user.created", kafkaPublishOutcomeSuccess, now)
	metrics.observeConsumed("events", "orders", kafkaConsumeOutcomeAcked)
	metrics.observeHandler("events", "orders", kafkaHandlerOutcomeSuccess, now)

	assertMetricLabels(t, reg, "kafka_published_total", []string{"outcome", "routing_key", "topic"})
	assertMetricLabels(t, reg, "kafka_publish_duration_seconds", []string{"outcome", "routing_key", "topic"})
	assertMetricLabels(t, reg, "kafka_consumed_total", []string{"group", "outcome", "topic"})
	assertMetricLabels(t, reg, "kafka_handler_duration_seconds", []string{"group", "outcome", "topic"})
}

func TestKafkaMetrics_RawLabelsOptIn(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg), WithRawRouteLabels())
	metrics.observePublish("events", "user.created", kafkaPublishOutcomeSuccess, time.Now())

	mfs, err := reg.Gather()
	require.NoError(t, err)
	found := false
	for _, mf := range mfs {
		if mf.GetName() != "kafka_published_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := labelValueMap(m.GetLabel())
			if labels["topic"] == "events" && labels["routing_key"] == "user.created" {
				found = true
			}
		}
	}
	assert.True(t, found, "raw labels must surface real topic / routing key when opted in")
}

func TestKafkaMetrics_OpaqueLabelsDefault(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))
	metrics.observePublish("tenant-42.events", "user-99.created", kafkaPublishOutcomeSuccess, time.Now())

	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != "kafka_published_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := labelValueMap(m.GetLabel())
			assert.NotEqual(t, "tenant-42.events", labels["topic"])
			assert.NotEqual(t, "user-99.created", labels["routing_key"])
		}
	}
}

func TestKafkaMetrics_WithRegisterer_NilPanics(t *testing.T) {
	assert.Panics(t, func() { WithRegisterer(nil) })
}

func TestKafkaMetrics_NilOptionPanics(t *testing.T) {
	assert.Panics(t, func() { NewMetrics(nil) })
}

func assertMetricLabels(t *testing.T, reg *prometheus.Registry, name string, want []string) {
	t.Helper()
	mfs, err := reg.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		require.NotEmpty(t, mf.GetMetric(), "metric %s has no samples", name)
		got := []string{}
		for _, l := range mf.GetMetric()[0].GetLabel() {
			got = append(got, l.GetName())
		}
		sort.Strings(got)
		assert.Equal(t, want, got, "metric %s labels", name)
		return
	}
	t.Fatalf("metric %s not registered", name)
}

func labelValueMap(labels []*dto.LabelPair) map[string]string {
	out := make(map[string]string, len(labels))
	for _, l := range labels {
		out[l.GetName()] = l.GetValue()
	}
	return out
}
