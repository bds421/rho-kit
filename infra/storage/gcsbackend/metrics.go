package gcsbackend

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// GCSMetrics holds Prometheus collectors for GCS storage operation monitoring.
type GCSMetrics struct {
	opDuration *prometheus.HistogramVec
	opErrors   *prometheus.CounterVec
}

// NewGCSMetrics creates and registers GCS metrics with the given registerer.
// If reg is nil, prometheus.DefaultRegisterer is used.
func NewGCSMetrics(reg prometheus.Registerer) *GCSMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &GCSMetrics{
		opDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "storage",
				Subsystem: "gcs",
				Name:      "operation_duration_seconds",
				Help:      "Duration of GCS storage operations.",
				Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
			[]string{"instance", "operation"},
		),
		opErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "storage",
				Subsystem: "gcs",
				Name:      "operation_errors_total",
				Help:      "Total GCS operation errors.",
			},
			[]string{"instance", "operation"},
		),
	}

	m.opDuration = promutil.MustRegisterOrGet(reg, m.opDuration)
	m.opErrors = promutil.MustRegisterOrGet(reg, m.opErrors)

	return m
}

var defaultGCSMetrics = NewGCSMetrics(nil)

// now returns the current time. A variable so tests can override it.
var now = time.Now

func (m *GCSMetrics) observeOp(instance, op string, start time.Time, err error) {
	m.opDuration.WithLabelValues(instance, op).Observe(time.Since(start).Seconds())
	if err != nil {
		m.opErrors.WithLabelValues(instance, op).Inc()
	}
}
