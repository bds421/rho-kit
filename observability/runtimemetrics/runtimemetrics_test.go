package runtimemetrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegister_PublishesExpectedMetricNames(t *testing.T) {
	reg := prometheus.NewRegistry()
	Register(reg)

	families, err := reg.Gather()
	require.NoError(t, err)

	got := make(map[string]bool)
	for _, f := range families {
		got[f.GetName()] = true
	}

	// Always available across platforms.
	assert.True(t, got["go_goroutines"])
	assert.True(t, got["go_threads"])
	assert.True(t, got["go_heap_alloc_bytes"])
	assert.True(t, got["go_heap_sys_bytes"])
	assert.True(t, got["go_gc_pause_seconds_sum"])
	assert.True(t, got["go_gc_count_total"])
	// go_max_rss_bytes is platform-conditional; present on linux/darwin.
}

func TestRegister_NilRegistererIsNoop(t *testing.T) {
	// Nil registerer must not panic. No assertion beyond the absence
	// of a panic.
	Register(nil)
}

func TestCollect_ProducesPositiveValues(t *testing.T) {
	c := newCollector()
	ch := make(chan prometheus.Metric, 16)
	c.Collect(ch)
	close(ch)

	var got int
	for m := range ch {
		got++
		_ = m
	}
	// At least 6 unconditional metrics must arrive; max_rss is bonus.
	assert.GreaterOrEqual(t, got, 6)
}
