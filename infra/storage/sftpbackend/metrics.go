package sftpbackend

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/promutil"
)

// SFTPMetrics holds Prometheus collectors for SFTP storage operation monitoring.
type SFTPMetrics struct {
	opDuration        *prometheus.HistogramVec
	opErrors          *prometheus.CounterVec
	connectionHealthy *prometheus.GaugeVec
}

// NewSFTPMetrics creates and registers SFTP metrics with the given registerer.
// If reg is nil, prometheus.DefaultRegisterer is used.
func NewSFTPMetrics(reg prometheus.Registerer) *SFTPMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &SFTPMetrics{
		opDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "storage",
				Subsystem: "sftp",
				Name:      "operation_duration_seconds",
				Help:      "Duration of SFTP storage operations.",
				Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
			[]string{"instance", "operation"},
		),
		opErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "storage",
				Subsystem: "sftp",
				Name:      "operation_errors_total",
				Help:      "Total SFTP operation errors.",
			},
			[]string{"instance", "operation"},
		),
		connectionHealthy: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "storage",
				Subsystem: "sftp",
				Name:      "connection_healthy",
				Help:      "Whether the SFTP connection is healthy (1) or not (0).",
			},
			[]string{"instance"},
		),
	}

	promutil.RegisterCollector(reg, m.opDuration)
	promutil.RegisterCollector(reg, m.opErrors)
	promutil.RegisterCollector(reg, m.connectionHealthy)

	return m
}

var defaultSFTPMetrics = NewSFTPMetrics(nil)

// now returns the current time. A variable so tests can override it.
var now = time.Now

func (m *SFTPMetrics) observeOp(instance, op string, start time.Time, err error) {
	m.opDuration.WithLabelValues(instance, op).Observe(time.Since(start).Seconds())
	if err != nil {
		m.opErrors.WithLabelValues(instance, op).Inc()
	}
}
