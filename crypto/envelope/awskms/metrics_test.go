package awskms

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestNewMetricsReuseOnAlreadyRegistered confirms that building Metrics
// twice against the same registerer reuses the live collector instead of
// crashing — the documented per-subtest wiring contract.
func TestNewMetricsReuseOnAlreadyRegistered(t *testing.T) {
	reg := prometheus.NewRegistry()

	first := NewMetrics(WithRegisterer(reg))
	second := NewMetrics(WithRegisterer(reg))

	if first.requestErrors != second.requestErrors {
		t.Fatal("repeated NewMetrics must reuse the existing registered collector")
	}
}

// TestNewMetricsPanicsOnConflictingRegistration guards against the silent
// gap where a non-AlreadyRegistered registration error (e.g. a collector
// already registered under the same fully-qualified name with different
// label names) is swallowed, leaving NewMetrics handing back an
// unregistered counter that is never scraped. This is exactly the failure
// mode WithRegisterer's nil-panic comment promises to prevent, so the
// conflict must fail loudly rather than at scrape time.
func TestNewMetricsPanicsOnConflictingRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()

	// Pre-register a collector with the same fully-qualified name
	// (awskms_request_errors_total) but DIFFERENT label names. Prometheus
	// rejects this with a plain error, not AlreadyRegisteredError.
	conflicting := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "awskms",
			Name:      "request_errors_total",
			Help:      "conflicting shape",
		},
		[]string{"different", "labels"},
	)
	if err := reg.Register(conflicting); err != nil {
		t.Fatalf("pre-register conflicting collector: %v", err)
	}

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("NewMetrics must panic when registration fails with a non-AlreadyRegistered error")
		}
	}()

	NewMetrics(WithRegisterer(reg))
}
