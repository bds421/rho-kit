package vaulttransit

import (
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestMetricsRecordClassifiedProviderError(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(WithRegisterer(reg))
	k, err := NewKEK(newTestClient(t, http.NotFoundHandler()), Config{KeyName: "key"}, WithMetrics(m))
	if err != nil {
		t.Fatalf("NewKEK: %v", err)
	}
	_ = k.classifyVaultError("decrypt", respErr(503))
	if got := testutil.ToFloat64(m.requestErrors.WithLabelValues("503", "decrypt")); got != 1 {
		t.Fatalf("request_errors_total = %v, want 1", got)
	}
}

func TestNewMetricsReusesRegisteredCollector(t *testing.T) {
	reg := prometheus.NewRegistry()
	first := NewMetrics(WithRegisterer(reg))
	second := NewMetrics(WithRegisterer(reg))
	if first.requestErrors != second.requestErrors {
		t.Fatal("repeated NewMetrics must reuse the registered collector")
	}
}

func TestWithMetricsNilPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("WithMetrics(nil) must panic")
		}
	}()
	_ = WithMetrics(nil)
}
