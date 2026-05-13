package s3backend

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// Metrics holds Prometheus collectors for S3 storage operation monitoring.
type Metrics struct {
	opDuration *prometheus.HistogramVec
	opErrors   *prometheus.CounterVec
}

// NewMetrics creates and registers S3 metrics with the given registerer.
// If reg is nil, prometheus.DefaultRegisterer is used.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &Metrics{
		opDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "storage",
				Subsystem: "s3",
				Name:      "operation_duration_seconds",
				Help:      "Duration of S3 storage operations.",
				Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
			[]string{"instance", "operation"},
		),
		opErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "storage",
				Subsystem: "s3",
				Name:      "operation_errors_total",
				Help:      "Total S3 operation errors.",
			},
			[]string{"instance", "operation"},
		),
	}

	m.opDuration = promutil.MustRegisterOrGet(reg, m.opDuration)
	m.opErrors = promutil.MustRegisterOrGet(reg, m.opErrors)

	return m
}

var defaultMetrics = sync.OnceValue(func() *Metrics { return NewMetrics(nil) })

// now returns the current time. A variable so tests can override it.
var now = time.Now

func (m *Metrics) observeOp(instance, op string, start time.Time, err error) {
	m.opDuration.WithLabelValues(instance, op).Observe(time.Since(start).Seconds())
	if err != nil {
		m.opErrors.WithLabelValues(instance, op).Inc()
	}
}
