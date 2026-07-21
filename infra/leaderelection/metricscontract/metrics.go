// Package metricscontract owns the shared Prometheus descriptor shape used by
// rho-kit's leader-election backends. It is public only because the backends
// are separate Go modules; applications should use each backend's NewMetrics.
package metricscontract

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// New registers or reuses the shared callback-drain collectors.
func New(reg prometheus.Registerer) (*prometheus.HistogramVec, *prometheus.CounterVec) {
	duration := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "leaderelection",
		Name:      "callback_drain_seconds",
		Help:      "Time waiting for the OnAcquired callback to return after leadership ended, by backend, target, and drain state.",
		Buckets:   []float64{1, 5, 10, 30, 60, 120, 300},
	}, []string{"backend", "target", "state"})
	warns := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "leaderelection",
		Name:      "callback_drain_warn_total",
		Help:      "Total warn ticks emitted while waiting for the OnAcquired callback to drain after leadership ended.",
	}, []string{"backend", "target"})
	return promutil.MustRegisterOrGet(reg, duration), promutil.MustRegisterOrGet(reg, warns)
}
