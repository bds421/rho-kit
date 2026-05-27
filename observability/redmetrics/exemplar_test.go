package redmetrics_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"go.opentelemetry.io/otel/trace"

	"github.com/bds421/rho-kit/observability/v2/redmetrics"
)

// fakeSpanCtx wraps a deterministic SpanContext so tests don't need a
// real OTel tracer provider.
func fakeSpanCtx(t *testing.T) context.Context {
	t.Helper()
	tid, err := trace.TraceIDFromHex("00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("trace ID parse: %v", err)
	}
	sid, err := trace.SpanIDFromHex("0011223344556677")
	if err != nil {
		t.Fatalf("span ID parse: %v", err)
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	return trace.ContextWithSpanContext(context.Background(), sc)
}

func TestTraceLabels_NoSpan(t *testing.T) {
	labels := redmetrics.TraceLabels(context.Background())
	if labels != nil {
		t.Fatalf("expected nil labels, got %v", labels)
	}
}

func TestTraceLabels_WithSpan(t *testing.T) {
	labels := redmetrics.TraceLabels(fakeSpanCtx(t))
	if labels["trace_id"] != "00112233445566778899aabbccddeeff" {
		t.Fatalf("trace_id mismatch: %v", labels)
	}
	if labels["span_id"] != "0011223344556677" {
		t.Fatalf("span_id mismatch: %v", labels)
	}
}

func TestHTTPMetrics_ExemplarsAttachWhenEnabled(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := redmetrics.NewHTTP(
		redmetrics.WithHTTPRegisterer(reg),
		redmetrics.WithHTTPNamespace("x"),
		redmetrics.WithHTTPExemplars(),
	)
	mw := m.Middleware(func(*http.Request) string { return "/echo" })
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/echo", nil).WithContext(fakeSpanCtx(t))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	mf := gatherFamily(t, reg, "x_http_request_duration_seconds")
	exemplar := firstExemplar(mf)
	if exemplar == nil {
		t.Fatalf("expected exemplar attached, none found")
	}
	got := map[string]string{}
	for _, lp := range exemplar.GetLabel() {
		got[lp.GetName()] = lp.GetValue()
	}
	if got["trace_id"] != "00112233445566778899aabbccddeeff" {
		t.Fatalf("exemplar trace_id mismatch: %v", got)
	}
	if got["span_id"] != "0011223344556677" {
		t.Fatalf("exemplar span_id mismatch: %v", got)
	}
}

func TestHTTPMetrics_NoExemplarsWhenDisabled(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := redmetrics.NewHTTP(
		redmetrics.WithHTTPRegisterer(reg),
		redmetrics.WithHTTPNamespace("nox"),
		// no WithHTTPExemplars
	)
	mw := m.Middleware(func(*http.Request) string { return "/echo" })
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/echo", nil).WithContext(fakeSpanCtx(t))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	mf := gatherFamily(t, reg, "nox_http_request_duration_seconds")
	if exemplar := firstExemplar(mf); exemplar != nil {
		t.Fatalf("expected no exemplar when option omitted, got %v", exemplar)
	}
}

func TestHTTPMetrics_NoExemplarWithoutActiveSpan(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := redmetrics.NewHTTP(
		redmetrics.WithHTTPRegisterer(reg),
		redmetrics.WithHTTPNamespace("y"),
		redmetrics.WithHTTPExemplars(),
	)
	mw := m.Middleware(func(*http.Request) string { return "/" })
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil) // no span on ctx
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	mf := gatherFamily(t, reg, "y_http_request_duration_seconds")
	if exemplar := firstExemplar(mf); exemplar != nil {
		t.Fatalf("expected no exemplar without active span, got %v", exemplar)
	}
}

func TestBatchMetrics_ObserveDurationWithExemplar(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := redmetrics.NewBatch("nightly",
		redmetrics.WithBatchRegisterer(reg),
		redmetrics.WithBatchNamespace("z"),
		redmetrics.WithBatchExemplars(),
	)
	m.ObserveDuration(fakeSpanCtx(t), "job-a", 2.5)

	mf := gatherFamily(t, reg, "z_nightly_run_duration_seconds")
	if exemplar := firstExemplar(mf); exemplar == nil {
		t.Fatalf("expected exemplar on batch observation, got nil")
	}
}

func TestBatchMetrics_ObserveDurationNoExemplarFallback(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := redmetrics.NewBatch("nightly",
		redmetrics.WithBatchRegisterer(reg),
		redmetrics.WithBatchNamespace("nofb"),
	) // no exemplars
	m.ObserveDuration(context.Background(), "job-b", 1.0)

	mf := gatherFamily(t, reg, "nofb_nightly_run_duration_seconds")
	if exemplar := firstExemplar(mf); exemplar != nil {
		t.Fatalf("expected no exemplar when batch opt omitted, got %v", exemplar)
	}
}

func gatherFamily(t *testing.T, reg *prometheus.Registry, name string) *dto.MetricFamily {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, fam := range families {
		if strings.EqualFold(fam.GetName(), name) {
			return fam
		}
	}
	t.Fatalf("metric family %q not found; got %v", name, familyNames(families))
	return nil
}

func familyNames(families []*dto.MetricFamily) []string {
	out := make([]string, 0, len(families))
	for _, f := range families {
		out = append(out, f.GetName())
	}
	return out
}

func firstExemplar(mf *dto.MetricFamily) *dto.Exemplar {
	for _, m := range mf.GetMetric() {
		hist := m.GetHistogram()
		if hist == nil {
			continue
		}
		for _, b := range hist.GetBucket() {
			if ex := b.GetExemplar(); ex != nil {
				return ex
			}
		}
	}
	return nil
}
