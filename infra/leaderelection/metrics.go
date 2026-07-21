package leaderelection

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// NewCallbackDrainMetrics registers or reuses the shared callback-drain
// collectors used by all leader-election backends. It is exported because the
// backend implementations live in separate Go modules; applications should
// use each backend's NewMetrics constructor instead.
func NewCallbackDrainMetrics(reg prometheus.Registerer) (*prometheus.HistogramVec, *prometheus.CounterVec) {
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
