package etcd

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
// callback-drain watchdog records into. Unexported so callers cannot
// accidentally pass a foreign implementation — wire concrete metrics
// through [WithMetrics] using [NewMetrics] (or omit the option for a
// silent no-op).
type callbackDrainMetrics interface {
	observeDrainDuration(d time.Duration, election, state string)
	observeDrainWarn(election string)
}

// Metrics holds Prometheus collectors that the elector emits while
// waiting for [leaderelection.Callbacks.OnAcquired] to drain after
// leadership ended.
//
// The label set is intentionally bounded to (election, state). The
// `election` label is the operator-configured key prefix; it is
// validated via [promutil.ValidateStaticLabelValue] at construction
// time when [WithMetrics] is used so a misconfigured caller cannot
// inflate cardinality. The `state` label is a fixed enum
// (`pending` / `drained` / `timeout`).
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
		panic("leaderelection/etcd: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
	}
	return func(c *metricsConfig) { c.registerer = reg }
}

// NewMetrics creates and registers callback-drain metrics (labels {election,state}; incompatible with redislock/pgadvisory/k8slease on the same registerer — see package leaderelection docs).
// Pass
// [WithRegisterer] to use a non-default registry. Repeated calls
// reuse already-registered collectors on the same registry.
func NewMetrics(opts ...MetricsOption) *Metrics {
	cfg := metricsConfig{registerer: prometheus.DefaultRegisterer}
	for _, opt := range opts {
		if opt == nil {
			panic("leaderelection/etcd: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	m := &Metrics{
		drainDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: "leaderelection",
			Name:      "callback_drain_seconds",
			Help:      "Time waiting for the OnAcquired callback to return after leadership ended, by election key and drain state (pending snapshot, terminal drained observation, or timeout).",
			Buckets:   []float64{1, 5, 10, 30, 60, 120, 300},
		}, []string{"election", "state"}),
		drainWarns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: "leaderelection",
			Name:      "callback_drain_warn_total",
			Help:      "Total warn ticks emitted while waiting for the OnAcquired callback to drain after leadership ended.",
		}, []string{"election"}),
	}
	m.drainDuration = promutil.MustRegisterOrGet(cfg.registerer, m.drainDuration)
	m.drainWarns = promutil.MustRegisterOrGet(cfg.registerer, m.drainWarns)
	return m
}

func (m *Metrics) observeDrainDuration(d time.Duration, election, state string) {
	if m == nil {
		return
	}
	m.drainDuration.WithLabelValues(election, state).Observe(d.Seconds())
}

func (m *Metrics) observeDrainWarn(election string) {
	if m == nil {
		return
	}
	m.drainWarns.WithLabelValues(election).Inc()
}

// validateMetricLabel panics if value would fail
// [promutil.ValidateStaticLabelValue]. Called at elector construction
// when [WithMetrics] is configured so misconfiguration surfaces at
// startup rather than the first label emission.
func validateMetricLabel(name, value string) {
	if err := promutil.ValidateStaticLabelValue(name, value); err != nil {
		panic("leaderelection/etcd: " + err.Error())
	}
}
