package circuitbreaker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMetrics_CountsCallsByOutcome pins the calls_total label
// taxonomy: success/fail/rejected_open are the three outcomes the
// caller-side dashboard alerts on.
func TestMetrics_CountsCallsByOutcome(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))

	cb := NewCircuitBreaker(2, time.Minute,
		WithName("test-breaker"),
		WithMetrics(metrics),
	)

	// One success.
	require.NoError(t, cb.ExecuteCtx(context.Background(), func(_ context.Context) error {
		return nil
	}))
	// Two failures to trip the breaker (threshold=2).
	_ = cb.ExecuteCtx(context.Background(), func(_ context.Context) error { return errors.New("boom") })
	_ = cb.ExecuteCtx(context.Background(), func(_ context.Context) error { return errors.New("boom") })

	// Next call rejected by open circuit.
	err := cb.ExecuteCtx(context.Background(), func(_ context.Context) error { return nil })
	assert.ErrorIs(t, err, ErrCircuitOpen)

	got := testutil.CollectAndCount(metrics.calls)
	assert.GreaterOrEqual(t, got, 3, "expected at least three outcome buckets recorded")

	assert.Equal(t, 1.0, testutil.ToFloat64(metrics.calls.WithLabelValues("test-breaker", outcomeSuccess)))
	assert.Equal(t, 2.0, testutil.ToFloat64(metrics.calls.WithLabelValues("test-breaker", outcomeFail)))
	assert.Equal(t, 1.0, testutil.ToFloat64(metrics.calls.WithLabelValues("test-breaker", outcomeRejectedOpen)))
}

// TestMetrics_RecordsStateChange verifies the closed→open transition
// emits one increment on the state_changes counter. The cooldown
// period in this test is set short so the breaker doesn't half-open
// during the assertion window.
func TestMetrics_RecordsStateChange(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))

	cb := NewCircuitBreaker(1, time.Hour,
		WithName("transition"),
		WithMetrics(metrics),
	)
	_ = cb.ExecuteCtx(context.Background(), func(_ context.Context) error {
		return errors.New("trip")
	})

	got := testutil.ToFloat64(metrics.stateChanges.WithLabelValues("transition", string(StateClosed), string(StateOpen)))
	assert.Equal(t, 1.0, got, "expected one closed→open transition recorded")
}

// TestMetrics_RecordsBeforeCallerCallback proves that the metric
// recording runs before the caller's OnStateChange callback, so a
// panicking caller callback does not suppress the metric.
func TestMetrics_RecordsBeforeCallerCallback(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))

	// Caller's callback always panics. Without ordering guarantees the
	// metric would never increment; the wave-186 design records first.
	cb := NewCircuitBreaker(1, time.Hour,
		WithName("ordering"),
		WithMetrics(metrics),
		WithOnStateChange(func(_ string, _, _ State) {
			panic("caller callback panicked")
		}),
	)
	defer func() { _ = recover() }() //nolint:errcheck // intentional swallow

	_ = cb.ExecuteCtx(context.Background(), func(_ context.Context) error {
		return errors.New("trip")
	})

	got := testutil.ToFloat64(metrics.stateChanges.WithLabelValues("ordering", string(StateClosed), string(StateOpen)))
	assert.Equal(t, 1.0, got, "metric must be recorded before the panicking caller callback runs")
}

// TestMetrics_NilSafe verifies a nil *Metrics receiver is safe so the
// helper paths in Execute/ExecuteCtx can call recordCall without
// guarding for nil at every call site.
func TestMetrics_NilSafe(t *testing.T) {
	var m *Metrics
	assert.NotPanics(t, func() {
		m.recordCall("x", outcomeSuccess)
		m.recordStateChange("x", StateClosed, StateOpen)
	})
}

// TestMetrics_NameFollowsKitConvention pins the wire-form metric
// names so dashboards built against them stay valid through future
// kit refactors. Force-observes one labelset on each counter so
// reg.Gather emits the metric family even though Prometheus elides
// untouched vec children.
func TestMetrics_NameFollowsKitConvention(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))
	metrics.recordCall("naming-probe", outcomeSuccess)
	metrics.recordStateChange("naming-probe", StateClosed, StateOpen)

	mfs, err := reg.Gather()
	require.NoError(t, err)
	names := map[string]bool{}
	for _, mf := range mfs {
		names[mf.GetName()] = true
	}
	for _, expected := range []string{
		"circuitbreaker_state_changes_total",
		"circuitbreaker_calls_total",
	} {
		assert.True(t, names[expected], "missing %q; got %v", expected, mapKeys(names))
	}
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestMetrics_HelpStringsMentionOutcomes is a smoke test that catches
// help-text drift — operators rely on these strings when they grep
// for "what counts as fail vs rejected_open" inside the breaker
// metric.
func TestMetrics_HelpStringsMentionOutcomes(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))
	metrics.recordCall("help-probe", outcomeSuccess)
	mfs, err := reg.Gather()
	require.NoError(t, err)

	saw := false
	for _, mf := range mfs {
		if mf.GetName() == "circuitbreaker_calls_total" {
			saw = true
			assert.True(t,
				strings.Contains(mf.GetHelp(), "rejected_open"),
				"calls_total help should mention rejected_open")
		}
	}
	assert.True(t, saw, "calls_total family not gathered")
}
