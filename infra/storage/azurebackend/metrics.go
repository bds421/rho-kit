package azurebackend

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/observability/v2/promutil"
)

// AzureMetrics holds Prometheus collectors for Azure Blob Storage operation
// monitoring.
type AzureMetrics struct {
	opDuration *prometheus.HistogramVec
	opErrors   *prometheus.CounterVec
}

// NewAzureMetrics creates and registers Azure Blob Storage metrics with the
// given registerer. If reg is nil, prometheus.DefaultRegisterer is used.
func NewAzureMetrics(reg prometheus.Registerer) *AzureMetrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &AzureMetrics{
		opDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "storage",
				Subsystem: "azure",
				Name:      "operation_duration_seconds",
				Help:      "Duration of Azure Blob Storage operations.",
				Buckets:   []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
			},
			[]string{"instance", "operation"},
		),
		opErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "storage",
				Subsystem: "azure",
				Name:      "operation_errors_total",
				Help:      "Total Azure Blob Storage operation errors.",
			},
			[]string{"instance", "operation"},
		),
	}

	m.opDuration = promutil.MustRegisterOrGet(reg, m.opDuration)
	m.opErrors = promutil.MustRegisterOrGet(reg, m.opErrors)

	return m
}

var defaultAzureMetrics = NewAzureMetrics(nil)

// now returns the current time. A variable so tests can override it.
var now = time.Now

func (m *AzureMetrics) observeOp(instance, op string, start time.Time, err error) {
	m.opDuration.WithLabelValues(instance, op).Observe(time.Since(start).Seconds())
	if err != nil {
		m.opErrors.WithLabelValues(instance, op).Inc()
	}
}
