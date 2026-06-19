package s3backend

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/bds421/rho-kit/infra/v2/storage"
)

func TestNewMetricsReusesCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m1 := NewMetrics(WithRegisterer(reg))
	m2 := NewMetrics(WithRegisterer(reg))

	if m1.opDuration != m2.opDuration {
		t.Fatal("opDuration collector was not reused")
	}
	if m1.opErrors != m2.opErrors {
		t.Fatal("opErrors collector was not reused")
	}
}

func TestMetricsContract(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))
	start := time.Now().Add(-10 * time.Millisecond)

	metrics.observeOp("assets", "put", start, nil)
	metrics.observeOp("assets", "get", start, errors.New("boom"))

	assertMetricLabels(t, reg, "storage_s3_operation_duration_seconds", []string{"operation", "storage_instance"})
	assertMetricLabels(t, reg, "storage_s3_operation_errors_total", []string{"operation", "storage_instance"})

	if got := testutil.ToFloat64(metrics.opErrors.WithLabelValues("assets", "get")); got != 1 {
		t.Fatalf("get errors = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.opErrors.WithLabelValues("assets", "put")); got != 0 {
		t.Fatalf("put errors = %v, want 0", got)
	}
}

func TestMetricsNormalizeExpectedNotFound(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := NewMetrics(WithRegisterer(reg))
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

func TestWithMetricsRegistererRoutesToCustomRegistry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reg := prometheus.NewRegistry()

	client := &mockS3Client{
		deleteFn: func(_ context.Context, _ *s3.DeleteObjectInput) (*s3.DeleteObjectOutput, error) {
			return &s3.DeleteObjectOutput{}, nil
		},
	}
	b := NewWithClient(client, &mockPresigner{}, "bucket",
		WithInstance("custom"),
		WithMetricsRegisterer(reg),
	)

	if err := b.Delete(ctx, "file.txt"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// The op duration must be recorded in the custom registry, proving the
	// backend uses the metrics built from WithMetricsRegisterer rather than the
	// default-registry collectors created in the struct literal.
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	var found bool
	for _, mf := range families {
		if mf.GetName() == "storage_s3_operation_duration_seconds" {
			found = true
		}
	}
	if !found {
		t.Fatal("custom registry did not receive storage_s3_operation_duration_seconds; backend ignored WithMetricsRegisterer")
	}
}

func TestPresignRecordsMetrics(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	reg := prometheus.NewRegistry()

	// A presigner whose Get succeeds but whose Put fails, so we can assert both
	// the success path (no error counter) and the failure path (error counter)
	// are observed under the presign_get / presign_put operation labels.
	presigner := &mockPresigner{getURL: "https://example.com/get"}
	b := NewWithClient(&mockS3Client{}, presigner, "bucket",
		WithInstance("assets"),
		WithMetricsRegisterer(reg),
	)

	if _, err := b.PresignGetURL(ctx, "file.txt", time.Minute); err != nil {
		t.Fatalf("PresignGetURL: %v", err)
	}

	// Force the Put presign to fail.
	presigner.err = errors.New("signer down")
	if _, err := b.PresignPutURL(ctx, "file.txt", time.Minute, storage.ObjectMeta{}); err == nil {
		t.Fatal("PresignPutURL: expected error")
	}

	// Duration histograms are recorded for both ops (one observation each). The
	// error counter must be incremented only for the failing presign_put.
	if got := testutil.CollectAndCount(b.metrics.opDuration, "storage_s3_operation_duration_seconds"); got != 2 {
		t.Fatalf("duration series count = %v, want 2 (presign_get + presign_put)", got)
	}
	if got := testutil.ToFloat64(b.metrics.opErrors.WithLabelValues("assets", "presign_get")); got != 0 {
		t.Fatalf("presign_get errors = %v, want 0", got)
	}
	if got := testutil.ToFloat64(b.metrics.opErrors.WithLabelValues("assets", "presign_put")); got != 1 {
		t.Fatalf("presign_put errors = %v, want 1", got)
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
