package awskms

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// awskmsRequestErrors counts classified AWS KMS API errors by smithy error
// code and operation. Operators can alert on sustained
// ThrottlingException / KMSInternalException rates (capacity/health) and
// distinguish them from KeyUnavailable / Disabled spikes (operational
// misconfiguration). The metric is registered lazily on the default
// registerer the first time an error is observed.
var (
	metricsInit         sync.Once
	awskmsRequestErrors *prometheus.CounterVec
)

func initMetrics() {
	awskmsRequestErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "kit_awskms_request_errors_total",
			Help: "Total AWS KMS API errors observed by the awskms adapter, labeled by smithy error code and adapter operation.",
		},
		[]string{"code", "operation"},
	)
	_ = prometheus.DefaultRegisterer.Register(awskmsRequestErrors)
}

func recordAWSError(operation, code string) {
	if code == "" {
		code = "unknown"
	}
	if operation == "" {
		operation = "unknown"
	}
	metricsInit.Do(initMetrics)
	if awskmsRequestErrors == nil {
		return
	}
	awskmsRequestErrors.WithLabelValues(code, operation).Inc()
}
