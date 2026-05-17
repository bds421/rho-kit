package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMetrics_OutcomeOnFirstTrySuccess covers the happy path: fn
// succeeds on attempt 1, outcome=success, attempts=1.
func TestMetrics_OutcomeOnFirstTrySuccess(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))

	policy := Policy{
		MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
		Factor: 2, Name: "happy", Metrics: m,
	}
	err := DoWith(context.Background(), policy, func(_ context.Context) error { return nil })
	require.NoError(t, err)

	assert.Equal(t, 1.0, testutil.ToFloat64(m.outcomes.WithLabelValues("happy", outcomeSuccess)))
}

// TestMetrics_OutcomeExhaustedAfterMaxRetries: fn always fails;
// outcome=failed_exhausted after MaxRetries+1 attempts.
func TestMetrics_OutcomeExhaustedAfterMaxRetries(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))

	calls := 0
	policy := Policy{
		MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
		Factor: 2, Name: "exhaust", Metrics: m,
	}
	err := DoWith(context.Background(), policy, func(_ context.Context) error {
		calls++
		return errors.New("boom")
	})
	require.Error(t, err)
	assert.Equal(t, 3, calls, "MaxRetries=2 means 1 + 2 retries = 3 attempts")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.outcomes.WithLabelValues("exhaust", outcomeFailedExhausted)))
}

// TestMetrics_OutcomeCtxCancelled: ctx cancels before retries finish;
// outcome=failed_ctx_cancelled.
func TestMetrics_OutcomeCtxCancelled(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate cancellation

	policy := Policy{
		MaxRetries: 5, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
		Factor: 2, Name: "ctx", Metrics: m,
	}
	err := DoWith(ctx, policy, func(_ context.Context) error { return errors.New("nope") })
	require.Error(t, err)
	assert.Equal(t, 1.0, testutil.ToFloat64(m.outcomes.WithLabelValues("ctx", outcomeFailedCtxCancelled)))
}

// TestMetrics_OutcomeNonRetryable: RetryIf says no on the first
// error; the loop terminates without retrying. Our classifier groups
// this with exhausted (the underlying err can't be distinguished from
// MaxRetries-exceeded; the OnRetry callback handles the split if a
// caller needs it).
func TestMetrics_OutcomeNonRetryableGroupedUnderExhausted(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))

	calls := 0
	policy := Policy{
		MaxRetries: 5, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
		Factor: 2, Name: "nonretry", Metrics: m,
		RetryIf: func(_ error) bool { return false },
	}
	err := DoWith(context.Background(), policy, func(_ context.Context) error {
		calls++
		return errors.New("permanent")
	})
	require.Error(t, err)
	assert.Equal(t, 1, calls, "RetryIf=false means the first attempt is the only attempt")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.outcomes.WithLabelValues("nonretry", outcomeFailedExhausted)),
		"non-retryable currently shares the exhausted bucket")
}

// TestMetrics_AttemptHistogramRecordsTotal verifies the attempts
// histogram observes a value (not just the counter), so dashboards
// can build percentile views.
func TestMetrics_AttemptHistogramRecordsTotal(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))

	policy := Policy{
		MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
		Factor: 2, Name: "hist", Metrics: m,
	}
	calls := 0
	err := DoWith(context.Background(), policy, func(_ context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	require.NoError(t, err)
	// Histogram should have one sample with sum = 3 attempts.
	mfs, err := reg.Gather()
	require.NoError(t, err)
	var got float64
	for _, mf := range mfs {
		if mf.GetName() != "retry_attempts" {
			continue
		}
		for _, met := range mf.GetMetric() {
			if h := met.GetHistogram(); h != nil {
				got = h.GetSampleSum()
			}
		}
	}
	assert.Equal(t, 3.0, got, "histogram sum should equal total attempts (3 on success-after-2-failures)")
}

// TestMetrics_EmptyNameMapsToUnnamed pins the label-value contract
// for unnamed policies — otherwise an empty label would violate
// Prometheus client semantics on some backends.
func TestMetrics_EmptyNameMapsToUnnamed(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))

	policy := Policy{
		MaxRetries: 0, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
		Factor: 2, Metrics: m,
		// Name intentionally empty.
	}
	err := DoWith(context.Background(), policy, func(_ context.Context) error { return nil })
	require.NoError(t, err)
	assert.Equal(t, 1.0, testutil.ToFloat64(m.outcomes.WithLabelValues("unnamed", outcomeSuccess)))
}

// TestMetrics_InvalidNameMapsToInvalid pins the cardinality safety
// net: a Policy.Name that fails ValidateStaticLabelValue is recorded
// under "_invalid" instead of inflating series.
func TestMetrics_InvalidNameMapsToInvalid(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))

	policy := Policy{
		MaxRetries: 0, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
		Factor: 2, Metrics: m,
		Name: "has whitespace", // invalid: whitespace
	}
	err := DoWith(context.Background(), policy, func(_ context.Context) error { return nil })
	require.NoError(t, err)
	assert.Equal(t, 1.0, testutil.ToFloat64(m.outcomes.WithLabelValues("_invalid", outcomeSuccess)))
}

// TestMetrics_NilSafe verifies a nil receiver is safe so the
// recordOutcome defer in doWithPolicy is always callable.
func TestMetrics_NilSafe(t *testing.T) {
	var m *Metrics
	assert.NotPanics(t, func() {
		m.recordOutcome("x", outcomeSuccess, 1)
	})
}
