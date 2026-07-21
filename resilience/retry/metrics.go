package retry

import (
	"context"
	"errors"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Metrics holds the kit-side Prometheus collectors for retry
// observability. Construct via [NewMetrics]; wire into a [Policy]
// via the Metrics field. Safe for concurrent use across many
// policies — the `name` label distinguishes instances. Set
// [Policy.Name] for each policy you want to observe separately.
//
// The kit's wave-167 OTel tracing remains the canonical span stream
// for individual call observation; Metrics adds aggregated counters
// suited for alerting and dashboards.
type Metrics struct {
	outcomes *prometheus.CounterVec
	attempts *prometheus.HistogramVec
}

// MetricsOption configures [NewMetrics].
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for retry
// metrics. Nil panics so misconfiguration surfaces at startup.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("retry: WithRegisterer requires a non-nil registerer")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics constructs and registers the retry metric set.
//
// Metric names follow the wave-184 Namespace+Name convention:
//
//   - retry_outcomes_total{name, outcome}
//   - retry_attempts{name, outcome}    (histogram)
//
// outcome ∈ {success, failed_non_retryable, failed_exhausted,
// failed_ctx_cancelled}. attempts is the total fn() invocations a
// single Do/DoWith made before terminating — useful for alerting on
// "retries that succeed eventually but burn the budget".
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("retry: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	m := &Metrics{
		outcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "retry",
			Name:      "outcomes_total",
			Help:      "Total retry-loop terminations by name and outcome (success / failed_non_retryable / failed_exhausted / failed_ctx_cancelled).",
		}, []string{"name", "outcome"}),
		attempts: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "retry",
			Name:      "attempts",
			Help:      "Distribution of total fn() invocations per Do/DoWith call, by name and outcome.",
			Buckets:   []float64{1, 2, 3, 5, 8, 13, 21, 34},
		}, []string{"name", "outcome"}),
	}
	m.outcomes = promutil.MustRegisterOrGet(cfg.registerer, m.outcomes)
	m.attempts = promutil.MustRegisterOrGet(cfg.registerer, m.attempts)
	return m
}

const (
	outcomeSuccess            = "success"
	outcomeFailedNonRetryable = "failed_non_retryable"
	outcomeFailedExhausted    = "failed_exhausted"
	outcomeFailedCtxCancelled = "failed_ctx_cancelled"
)

// classifyRetryOutcome maps the terminal err and ctx into the bounded
// outcome label. The classification matches the loop body's return
// paths in doWithPolicy: RetryIf-rejected → non_retryable;
// MaxRetries/MaxElapsedTime → exhausted; retry ctx done →
// ctx_cancelled; nil err → success. Anything else is grouped under
// exhausted (the catch-all terminal path).
//
// Downstream timeouts that wrap context.DeadlineExceeded while the
// retry ctx itself is still alive are classified as exhausted, not
// ctx_cancelled — only the retry context's own cancellation counts
// as failed_ctx_cancelled.
func classifyRetryOutcome(err error, ctx context.Context) string {
	if err == nil {
		return outcomeSuccess
	}
	// Prefer the retry context's own state over the terminal error.
	// fn may wrap a per-attempt DeadlineExceeded from a downstream
	// sub-context even when the retry loop's ctx is still live.
	if ctx != nil && ctx.Err() != nil {
		return outcomeFailedCtxCancelled
	}
	// Bare context.Canceled without a live ctx argument is treated
	// as cancellation (Do with a cancelled Background-derived ctx
	// still passes that ctx in). DeadlineExceeded alone is not —
	// without a dead retry ctx it is an ordinary exhausted failure.
	if errors.Is(err, context.Canceled) {
		return outcomeFailedCtxCancelled
	}
	// The loop body returns the underlying err directly in the
	// non-retryable + exhausted paths; we can't distinguish them
	// from just the error value. Caller code that needs the split
	// can inspect via OnRetry. Group under exhausted as the
	// terminal-non-success bucket.
	return outcomeFailedExhausted
}

// recordOutcome is the nil-safe entry point doWithPolicy defers
// against. validateLabel guards against an unbounded Policy.Name
// inflating series cardinality; bad names are recorded under
// "_invalid" so the operator still sees the event.
//
// attempts is the total fn() invocations the call made. It is 0 only
// for the failed_ctx_cancelled outcome when ctx was already cancelled
// before the first invocation; that 0 falls below the histogram's
// smallest bucket (1) on purpose — the outcomes counter still records
// the cancellation, and a 0-attempt sample is the truthful "fn never
// ran" signal rather than a fabricated attempts=1.
func (m *Metrics) recordOutcome(name, outcome string, attempts int) {
	if m == nil {
		return
	}
	labelName := nameLabel(name)
	m.outcomes.WithLabelValues(labelName, outcome).Inc()
	m.attempts.WithLabelValues(labelName, outcome).Observe(float64(attempts))
}

func nameLabel(name string) string {
	if name == "" {
		return "unnamed"
	}
	if err := promutil.ValidateStaticLabelValue("name", name); err != nil {
		return "_invalid"
	}
	return name
}
