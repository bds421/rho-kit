package k8slease

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
	observeDrainDuration(d time.Duration, namespace, name, state string)
	observeDrainWarn(namespace, name string)
}

// Metrics holds Prometheus collectors that the elector emits while
// waiting for [leaderelection.Callbacks.OnAcquired] to drain after
// leadership ended.
//
// All leader-election backends share the labels {backend,target,state} so
// multiple adapters can register against the same Prometheus registry.
// target is the validated "namespace/name" Lease coordinate.
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
		panic("leaderelection/k8slease: WithRegisterer requires a non-nil registerer (omit the option for DefaultRegisterer)")
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
			panic("leaderelection/k8slease: NewMetrics option must not be nil")
		}
		opt(&cfg)
	}
	duration, warns := leaderelection.NewCallbackDrainMetrics(cfg.registerer)
	m := &Metrics{drainDuration: duration, drainWarns: warns}
	return m
}

func (m *Metrics) observeDrainDuration(d time.Duration, namespace, name, state string) {
	if m == nil {
		return
	}
	m.drainDuration.WithLabelValues("k8slease", namespace+"/"+name, state).Observe(d.Seconds())
}

func (m *Metrics) observeDrainWarn(namespace, name string) {
	if m == nil {
		return
	}
	m.drainWarns.WithLabelValues("k8slease", namespace+"/"+name).Inc()
}

// validateMetricLabel guards the operator-supplied Lease coordinates
// against accidental high-cardinality label injection.
func validateMetricLabel(field, value string) {
	if err := promutil.ValidateStaticLabelValue(field, value); err != nil {
		panic("leaderelection/k8slease: invalid metric " + field + " label: " + err.Error())
	}
}
