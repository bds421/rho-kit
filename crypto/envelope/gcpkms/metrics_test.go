package gcpkms

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestNewMetricsReuseOnAlreadyRegistered(t *testing.T) {
	reg := prometheus.NewRegistry()

	first := NewMetrics(WithRegisterer(reg))
	second := NewMetrics(WithRegisterer(reg))

	if first.requestErrors != second.requestErrors {
		t.Fatal("repeated NewMetrics must reuse the existing registered collector")
	}
}

func TestNewMetricsPanicsOnConflictingRegistration(t *testing.T) {
	reg := prometheus.NewRegistry()

	conflicting := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "gcpkms",
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

func TestWithMetricsNilPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("WithMetrics(nil) must panic")
		}
	}()
	_ = WithMetrics(nil)
}
