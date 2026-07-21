package etcd

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/infra/v2/leaderelection/metricscontract"
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
// All leader-election backends share the labels {backend,target,state} so
// multiple adapters can register against the same Prometheus registry.
// target is the validated operator-configured election prefix.
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

// NewMetrics creates and registers callback-drain metrics using the shared
// {backend,target,state} family shape. Pass
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
	duration, warns := metricscontract.New(cfg.registerer)
	m := &Metrics{drainDuration: duration, drainWarns: warns}
	return m
}

func (m *Metrics) observeDrainDuration(d time.Duration, election, state string) {
	if m == nil {
		return
	}
	m.drainDuration.WithLabelValues("etcd", election, state).Observe(d.Seconds())
}

func (m *Metrics) observeDrainWarn(election string) {
	if m == nil {
		return
	}
	m.drainWarns.WithLabelValues("etcd", election).Inc()
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
