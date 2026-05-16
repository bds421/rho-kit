package redisqueue

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

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

// TestNewMetrics_RegistersFullSurface pins the complete counter/gauge/
// histogram set so a future refactor that drops one of them is caught
// here rather than during production scrapes.
func TestNewMetrics_RegistersFullSurface(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))

	// Probe every series so Gather has something to enumerate.
	m.messagesEnqueued.WithLabelValues("probe").Inc()
	m.messagesProcessed.WithLabelValues("probe").Inc()
	m.messagesFailed.WithLabelValues("probe").Inc()
	m.messagesDeadLettered.WithLabelValues("probe").Inc()
	m.messagesRetried.WithLabelValues("probe").Inc()
	m.processingDuration.WithLabelValues("probe").Observe(0.1)
	m.processingDepth.WithLabelValues("probe").Set(1)
	m.queueDepth.WithLabelValues("probe").Set(1)
	m.dlqDepth.WithLabelValues("probe").Set(1)

	want := map[string]bool{
		"redis_queue_messages_enqueued_total":      false,
		"redis_queue_messages_processed_total":     false,
		"redis_queue_messages_failed_total":        false,
		"redis_queue_messages_dead_lettered_total": false,
		"redis_queue_messages_retried_total":       false,
		"redis_queue_processing_duration_seconds":  false,
		"redis_queue_processing_depth":             false,
		"redis_queue_queue_depth":                  false,
		"redis_queue_dlq_depth":                    false,
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range families {
		if _, ok := want[mf.GetName()]; ok {
			want[mf.GetName()] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("expected metric %q to be registered", name)
		}
	}
}
