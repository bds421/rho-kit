package redisqueue

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

// TestUpdateProcessingDepth_PollsAllThreeQueues pins the DLQ depth gauge
// onto the existing depth-poller: a single updateProcessingDepth call
// must update queue_depth, processing_depth, and dlq_depth without
// spawning a new goroutine. Misconfigured DLQ alerting goes unnoticed
// until the queue back-pressures — this regression test catches a
// future refactor that forgets to LLEN the dead-letter list.
func TestUpdateProcessingDepth_PollsAllThreeQueues(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	reg := prometheus.NewRegistry()
	q := NewQueue(client, WithMetricsRegisterer(reg))

	ctx := context.Background()
	queueName := "test-queue"
	processingQ := queueName + ":processing:" + q.consumerID
	deadQ := queueName + ":dead"

	require.NoError(t, client.LPush(ctx, queueName, "a", "b", "c").Err())
	require.NoError(t, client.LPush(ctx, processingQ, "p1", "p2").Err())
	require.NoError(t, client.LPush(ctx, deadQ, "d1", "d2", "d3", "d4").Err())

	q.updateProcessingDepth(ctx, queueName, processingQ, deadQ)

	label := queueMetricLabel(queueName)
	if got := testutil.ToFloat64(q.metrics.queueDepth.WithLabelValues(label)); got != 3 {
		t.Errorf("queue_depth = %v, want 3", got)
	}
	if got := testutil.ToFloat64(q.metrics.processingDepth.WithLabelValues(label)); got != 2 {
		t.Errorf("processing_depth = %v, want 2", got)
	}
	if got := testutil.ToFloat64(q.metrics.dlqDepth.WithLabelValues(label)); got != 4 {
		t.Errorf("dlq_depth = %v, want 4", got)
	}
}

// TestUpdateProcessingDepth_EmptyDLQName pins the safety guard: if a
// future caller passes an empty deadQ (defensive null), we still update
// the main and processing depths and skip the DLQ LLEN.
func TestUpdateProcessingDepth_EmptyDLQName(t *testing.T) {
	client := newTestClient(t)
	t.Cleanup(func() { _ = client.Close() })

	reg := prometheus.NewRegistry()
	q := NewQueue(client, WithMetricsRegisterer(reg))

	ctx := context.Background()
	queueName := "test-queue"
	processingQ := queueName + ":processing:" + q.consumerID

	require.NoError(t, client.LPush(ctx, queueName, "a").Err())

	q.updateProcessingDepth(ctx, queueName, processingQ, "")

	label := queueMetricLabel(queueName)
	if got := testutil.ToFloat64(q.metrics.queueDepth.WithLabelValues(label)); got != 1 {
		t.Errorf("queue_depth = %v, want 1", got)
	}
	if got := testutil.ToFloat64(q.metrics.dlqDepth.WithLabelValues(label)); got != 0 {
		t.Errorf("dlq_depth = %v, want 0 (skip on empty name)", got)
	}
}

// TestNewMetrics_RegistersDLQDepth confirms the gauge is registered with
// the expected label set so a future label-set rename surfaces here
// rather than in production scrapes.
func TestNewMetrics_RegistersDLQDepth(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))

	m.dlqDepth.WithLabelValues("probe").Set(7)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var found bool
	for _, mf := range families {
		if mf.GetName() == "redis_queue_dlq_depth" {
			found = true
			if len(mf.Metric) != 1 {
				t.Fatalf("expected 1 series, got %d", len(mf.Metric))
			}
			lbls := mf.Metric[0].Label
			if len(lbls) != 1 || lbls[0].GetName() != "queue" {
				t.Fatalf("unexpected labels: %+v", lbls)
			}
			if v := mf.Metric[0].GetGauge().GetValue(); v != 7 {
				t.Fatalf("gauge value = %v, want 7", v)
			}
		}
	}
	if !found {
		t.Fatal("redis_queue_dlq_depth was not registered")
	}
}
