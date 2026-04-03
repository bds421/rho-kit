package s3backend

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/promutil"
)

// S3Metrics holds Prometheus collectors for S3 storage operation monitoring.
type S3Metrics struct {
	opDuration *prometheus.HistogramVec
	opErrors   *prometheus.CounterVec
}

// NewS3Metrics creates and registers S3 metrics with the given registerer.
// If reg is nil, prometheus.DefaultRegisterer is used.
func NewS3Metrics(reg prometheus.Registerer) *S3Metrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &S3Metrics{
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

	promutil.RegisterCollector(reg, m.opDuration)
	promutil.RegisterCollector(reg, m.opErrors)

	return m
}

var defaultS3Metrics = NewS3Metrics(nil)

// now returns the current time. A variable so tests can override it.
var now = time.Now

func (m *S3Metrics) observeOp(instance, op string, start time.Time, err error) {
	m.opDuration.WithLabelValues(instance, op).Observe(time.Since(start).Seconds())
	if err != nil {
		m.opErrors.WithLabelValues(instance, op).Inc()
	}
}
