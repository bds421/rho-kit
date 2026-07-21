package pgadvisory

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/infra/v2/leaderelection"
	"github.com/bds421/rho-kit/observability/v2/promutil"
)

const (
	drainStatePending = "pending"
	drainStateDrained = "drained"
	drainStateTimeout = "timeout"
)

// callbackDrainMetrics is the minimal observation surface the
// holdLeadership drain watchdog records into. The interface is
// unexported so callers cannot accidentally pass a foreign metrics
// implementation — wire concrete metrics through [WithMetrics] using
// [NewMetrics] (or omit the option for a silent no-op).
type callbackDrainMetrics interface {
	observeDrainDuration(d time.Duration, key, state string)
	observeDrainWarn(key string)
}

// Metrics holds Prometheus collectors that the elector emits while
// waiting for [leaderelection.Callbacks.OnAcquired] to drain after
// leadership ends.
//
// All leader-election backends share the labels {backend,target,state} so
// multiple adapters can register against the same Prometheus registry.
// target is the validated operator-chosen advisory-lock key.
type Metrics struct {
	drainDuration *prometheus.HistogramVec
	drainWarns    *prometheus.CounterVec
}

// MetricsOption configures callback-drain metric construction.
type MetricsOption func(*metricsConfig)

type metricsConfig struct {
	registerer prometheus.Registerer
}

// WithRegisterer pins the Prometheus registerer used for the
// callback-drain metrics. When unset, [prometheus.DefaultRegisterer]
// is used.
func WithRegisterer(reg prometheus.Registerer) MetricsOption {
	if reg == nil {
		panic("leaderelection/pgadvisory: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics creates and registers callback-drain metrics using the shared
// {backend,target,state} family shape. Pass
// [WithRegisterer] to use a non-default registry. Repeated
// calls reuse already-registered collectors on the same registry.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("leaderelection/pgadvisory: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	duration, warns := leaderelection.NewCallbackDrainMetrics(cfg.registerer)
	m := &Metrics{drainDuration: duration, drainWarns: warns}
	return m
}

func (m *Metrics) observeDrainDuration(d time.Duration, key, state string) {
	if m == nil {
		return
	}
	m.drainDuration.WithLabelValues("pgadvisory", key, state).Observe(d.Seconds())
}

func (m *Metrics) observeDrainWarn(key string) {
	if m == nil {
		return
	}
	m.drainWarns.WithLabelValues("pgadvisory", key).Inc()
}

// validateMetricKeyLabel guards the operator-supplied leader-election
// key against accidental high-cardinality label injection. Keys are
// developer-chosen identifiers (e.g. "tenant-sweeper"), and the
// elector keeps the raw key in logs only — never in metric labels
// unless it passes the same validation as other static label values.
func validateMetricKeyLabel(key string) {
	if err := promutil.ValidateStaticLabelValue("leader key", key); err != nil {
		panic("leaderelection/pgadvisory: invalid metric key label: " + err.Error())
	}
}
