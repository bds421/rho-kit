package redislock

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

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
// The label set is deliberately small: key is the operator-chosen
// leadership key (validated via [promutil.ValidateStaticLabelValue] at
// construction time so a misconfigured caller cannot inflate
// cardinality), state is "pending" for the periodic warn snapshot or
// "drained" for the terminal observation.
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
		panic("leaderelection/redislock: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics creates and registers callback-drain metrics (labels {key,state}; incompatible with etcd/k8slease on the same registerer — see package leaderelection docs).
// Pass
// [WithRegisterer] to use a non-default registry. Repeated
// calls reuse already-registered collectors on the same registry.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("leaderelection/redislock: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	m := &Metrics{
		drainDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "leaderelection",
			Name:      "callback_drain_seconds",
			Help:      "Time waiting for the OnAcquired callback to return after leadership ended, by key and state (pending snapshot or terminal drained observation).",
			Buckets:   []float64{1, 5, 10, 30, 60, 120, 300},
		}, []string{"key", "state"}),
		drainWarns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "leaderelection",
			Name:      "callback_drain_warn_total",
			Help:      "Total warn ticks emitted while waiting for the OnAcquired callback to drain after leadership ended.",
		}, []string{"key"}),
	}
	m.drainDuration = promutil.MustRegisterOrGet(cfg.registerer, m.drainDuration)
	m.drainWarns = promutil.MustRegisterOrGet(cfg.registerer, m.drainWarns)
	return m
}

func (m *Metrics) observeDrainDuration(d time.Duration, key, state string) {
	if m == nil {
		return
	}
	m.drainDuration.WithLabelValues(key, state).Observe(d.Seconds())
}

func (m *Metrics) observeDrainWarn(key string) {
	if m == nil {
		return
	}
	m.drainWarns.WithLabelValues(key).Inc()
}

// validateMetricKeyLabel guards the operator-supplied leader-election
// key against accidental high-cardinality label injection. Keys are
// developer-chosen identifiers (e.g. "tenant-sweeper"), and the
// elector keeps the raw key in logs only — never in metric labels
// unless it passes the same validation as other static label values.
func validateMetricKeyLabel(key string) {
	if err := promutil.ValidateStaticLabelValue("leader key", key); err != nil {
		panic("leaderelection/redislock: invalid metric key label: " + err.Error())
	}
}
