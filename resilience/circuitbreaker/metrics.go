package circuitbreaker

import (
	"context"
	"errors"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Metrics holds the kit-side Prometheus collectors for breaker
// observability. Construct via [NewMetrics]; wire into a breaker via
// [WithMetrics]. Safe for concurrent use across many breakers — the
// `name` label distinguishes instances. Operators who want
// per-instance dashboards should give each breaker a distinct
// [WithName].
//
// The kit's wave-167 OTel tracing remains the canonical event stream
// for individual call observation; Metrics adds aggregated counters
// suited for alerting and dashboards.
type Metrics struct {
	stateChanges *prometheus.CounterVec
	calls        *prometheus.CounterVec
}

// MetricsOption configures [NewMetrics].
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for breaker
// metrics. Nil panics so misconfiguration surfaces at startup.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("circuitbreaker: WithRegisterer requires a non-nil registerer")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics constructs and registers the breaker metric set.
//
// Metric names follow the wave-184 Namespace+Name convention:
//
//   - circuitbreaker_state_changes_total{name, from, to}
//   - circuitbreaker_calls_total{name, outcome}
//
// outcome ∈ {success, fail, rejected_open}. rejected_open is the
// fast-fail path when the breaker is already open; fail covers every
// other non-nil err the breaker counts as a failure per its
// IsSuccessful predicate.
//
// All labels are bounded developer-defined values (name validated
// via [promutil.ValidateStaticLabelValue] in [WithName], from/to/
// outcome drawn from the finite enums above) — no caller-supplied
// dimensions enter the label set.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("circuitbreaker: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	m := &Metrics{
		stateChanges: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "circuitbreaker",
			Name:      "state_changes_total",
			Help:      "Total circuit breaker state transitions by name, from-state, and to-state.",
		}, []string{"name", "from", "to"}),
		calls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "circuitbreaker",
			Name:      "calls_total",
			Help:      "Total circuit breaker calls by name and outcome (success / fail / rejected_open).",
		}, []string{"name", "outcome"}),
	}
	m.stateChanges = promutil.MustRegisterOrGet(cfg.registerer, m.stateChanges)
	m.calls = promutil.MustRegisterOrGet(cfg.registerer, m.calls)
	return m
}

const (
	outcomeSuccess      = "success"
	outcomeFail         = "fail"
	outcomeRejectedOpen = "rejected_open"
)

// callOutcome maps the post-Execute error into the bounded outcome
// label. ErrCircuitOpen has its own bucket so dashboards can separate
// "downstream is broken and the breaker is protecting us" from
// "downstream returned an error this attempt".
func callOutcome(err error) string {
	switch {
	case err == nil:
		return outcomeSuccess
	case errors.Is(err, ErrCircuitOpen):
		return outcomeRejectedOpen
	case errors.Is(err, context.Canceled):
		// The default success predicate already excludes caller-driven
		// cancellation from the breaker's failure count; surface it
		// as success here too so the metric matches breaker accounting.
		return outcomeSuccess
	default:
		return outcomeFail
	}
}

// recordStateChange is a nil-safe wrapper so [NewCircuitBreaker] can
// call it without checking for nil metrics.
func (m *Metrics) recordStateChange(name string, from, to State) {
	if m == nil {
		return
	}
	m.stateChanges.WithLabelValues(name, string(from), string(to)).Inc()
}

// recordCall is a nil-safe wrapper so Execute / ExecuteCtx can call
// it unconditionally.
func (m *Metrics) recordCall(name, outcome string) {
	if m == nil {
		return
	}
	m.calls.WithLabelValues(name, outcome).Inc()
}
