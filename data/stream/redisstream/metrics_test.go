package redisstream

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRedisStreamProducerMetrics_ReusesCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m1 := NewProducerMetrics(reg)
	m2 := NewProducerMetrics(reg)

	assert.Same(t, m1.messagesProduced, m2.messagesProduced)
}

func TestRedisStreamConsumerMetrics_ReusesCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m1 := NewConsumerMetrics(reg)
	m2 := NewConsumerMetrics(reg)

	assert.Same(t, m1.messagesConsumed, m2.messagesConsumed)
	assert.Same(t, m1.messagesFailed, m2.messagesFailed)
	assert.Same(t, m1.messagesDeadLettered, m2.messagesDeadLettered)
	assert.Same(t, m1.processingDuration, m2.processingDuration)
	assert.Same(t, m1.pendingMessages, m2.pendingMessages)
}

func TestRedisStreamMetricsContract(t *testing.T) {
	reg := prometheus.NewRegistry()
	producerMetrics := NewProducerMetrics(reg)
	consumerMetrics := NewConsumerMetrics(reg)

	stream := streamMetricLabel("tenant-secret:events.high")
	group := groupMetricLabel("tenant-secret:workers.high")
	producerMetrics.messagesProduced.WithLabelValues(stream).Inc()
	consumerMetrics.messagesConsumed.WithLabelValues(stream, group).Inc()
	consumerMetrics.messagesFailed.WithLabelValues(stream, group).Inc()
	consumerMetrics.messagesDeadLettered.WithLabelValues(stream, group).Inc()
	consumerMetrics.processingDuration.WithLabelValues(stream, group).Observe((10 * time.Millisecond).Seconds())
	consumerMetrics.pendingMessages.WithLabelValues(stream, group).Set(3)

	assertMetricLabels(t, reg, "redis_stream_messages_produced_total", []string{"stream"})
	assertMetricLabels(t, reg, "redis_stream_messages_consumed_total", []string{"group", "stream"})
	assertMetricLabels(t, reg, "redis_stream_messages_failed_total", []string{"group", "stream"})
	assertMetricLabels(t, reg, "redis_stream_messages_dead_lettered_total", []string{"group", "stream"})
	assertMetricLabels(t, reg, "redis_stream_processing_duration_seconds", []string{"group", "stream"})
	assertMetricLabels(t, reg, "redis_stream_pending_messages", []string{"group", "stream"})

	if got := testutil.ToFloat64(producerMetrics.messagesProduced.WithLabelValues(stream)); got != 1 {
		t.Fatalf("produced = %v, want 1", got)
	}
	if got := testutil.ToFloat64(consumerMetrics.pendingMessages.WithLabelValues(stream, group)); got != 3 {
		t.Fatalf("pending = %v, want 3", got)
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
