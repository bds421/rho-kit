package s3backend

import (
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestNewMetricsReusesCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m1 := NewMetrics(reg)
	m2 := NewMetrics(reg)

	if m1.opDuration != m2.opDuration {
		t.Fatal("opDuration collector was not reused")
	}
	if m1.opErrors != m2.opErrors {
		t.Fatal("opErrors collector was not reused")
	}
}

func TestMetricsContract(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	start := time.Now().Add(-10 * time.Millisecond)

	metrics.observeOp("assets", "put", start, nil)
	metrics.observeOp("assets", "get", start, errors.New("boom"))

	assertMetricLabels(t, reg, "storage_s3_operation_duration_seconds", []string{"instance", "operation"})
	assertMetricLabels(t, reg, "storage_s3_operation_errors_total", []string{"instance", "operation"})

	if got := testutil.ToFloat64(metrics.opErrors.WithLabelValues("assets", "get")); got != 1 {
		t.Fatalf("get errors = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.opErrors.WithLabelValues("assets", "put")); got != 0 {
		t.Fatalf("put errors = %v, want 0", got)
	}
}

func TestMetricsNormalizeExpectedNotFound(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(reg)
	start := time.Now().Add(-10 * time.Millisecond)

	metrics.observeOp("assets", "delete", start, s3MetricErr(&types.NotFound{}))
	metrics.observeOp("assets", "exists", start, s3MetricErr(&types.NoSuchKey{}))

	if got := testutil.ToFloat64(metrics.opErrors.WithLabelValues("assets", "delete")); got != 0 {
		t.Fatalf("delete errors = %v, want 0", got)
	}
	if got := testutil.ToFloat64(metrics.opErrors.WithLabelValues("assets", "exists")); got != 0 {
		t.Fatalf("exists errors = %v, want 0", got)
	}
}

func assertMetricLabels(t *testing.T, reg *prometheus.Registry, family string, want []string) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range families {
		if mf.GetName() != family {
			continue
		}
		if len(mf.GetMetric()) == 0 {
			t.Fatalf("%s has no metrics", family)
		}
		labels := mf.GetMetric()[0].GetLabel()
		got := make([]string, 0, len(labels))
		for _, label := range labels {
			got = append(got, label.GetName())
		}
		if len(got) != len(want) {
			t.Fatalf("%s labels = %v, want %v", family, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("%s labels = %v, want %v", family, got, want)
			}
		}
		return
	}
	t.Fatalf("metric family %s not found", family)
}
