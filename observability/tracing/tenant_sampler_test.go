package tracing

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/bds421/rho-kit/core/v2/tenant"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestNewTenantSampler_NoOverridesMatchesLegacyTree(t *testing.T) {
	s, err := newTenantSampler(0.05, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	desc := s.Description()
	if !strings.Contains(desc, "ParentBased") || !strings.Contains(desc, "TraceIDRatioBased") {
		t.Fatalf("description = %q, want a parent-based ratio tree", desc)
	}
	if strings.Contains(desc, "TenantSampler") {
		t.Fatalf("description = %q, must not include TenantSampler when no overrides are set", desc)
	}
}

func TestNewTenantSampler_RejectsInvalidDefault(t *testing.T) {
	for _, rate := range []float64{-0.01, 1.5} {
		if _, err := newTenantSampler(rate, nil); err == nil {
			t.Fatalf("default rate %.2f should be rejected", rate)
		}
	}
}

func TestNewTenantSampler_RejectsInvalidOverride(t *testing.T) {
	for _, rate := range []float64{-0.01, 1.01} {
		_, err := newTenantSampler(0.5, map[string]float64{"alice": rate})
		if err == nil {
			t.Fatalf("override rate %.2f should be rejected", rate)
		}
	}
}

func TestNewTenantSampler_RejectsEmptyTenantID(t *testing.T) {
	_, err := newTenantSampler(0.5, map[string]float64{"": 0.5})
	if err == nil {
		t.Fatal("empty tenant ID should be rejected")
	}
}

func TestTenantSampler_OverrideRouteForKnownTenant(t *testing.T) {
	// 0% default, 100% for "vip" — every "vip"-tagged span must be
	// sampled, every other span must be dropped.
	s, err := newTenantSampler(0, map[string]float64{"vip": 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(s),
		sdktrace.WithSyncer(exporter),
	)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	tr := tp.Tracer("test")

	// Anonymous request — default sampler (0%) drops the span.
	_, anonSpan := tr.Start(context.Background(), "anon")
	anonSpan.End()

	// VIP tenant — override forces sampling.
	vipCtx, err := tenant.WithID(context.Background(), tenant.ID("vip"))
	if err != nil {
		t.Fatalf("tenant.WithID: %v", err)
	}
	_, vipSpan := tr.Start(vipCtx, "vip-op")
	vipSpan.End()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected exactly the VIP span to be exported; got %d spans", len(spans))
	}
	if got := spans[0].Name; got != "vip-op" {
		t.Fatalf("exported span name = %q, want %q", got, "vip-op")
	}
}

func TestTenantSampler_UnknownTenantUsesDefault(t *testing.T) {
	// 100% default, 0% for "noisy" — anonymous traffic is fully
	// sampled, noisy tenant is fully suppressed.
	s, err := newTenantSampler(1, map[string]float64{"noisy": 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(s),
		sdktrace.WithSyncer(exporter),
	)
	tr := tp.Tracer("test")

	// "noisy" tenant — must be dropped.
	noisyCtx, err := tenant.WithID(context.Background(), tenant.ID("noisy"))
	if err != nil {
		t.Fatalf("tenant.WithID: %v", err)
	}
	_, noisy := tr.Start(noisyCtx, "noisy-op")
	noisy.End()

	// Unknown tenant — falls through to default (100%).
	otherCtx, err := tenant.WithID(context.Background(), tenant.ID("other"))
	if err != nil {
		t.Fatalf("tenant.WithID: %v", err)
	}
	_, other := tr.Start(otherCtx, "other-op")
	other.End()

	// No tenant — falls through to default (100%).
	_, anon := tr.Start(context.Background(), "anon-op")
	anon.End()

	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans (other + anon); got %d", len(spans))
	}
	for _, s := range spans {
		if s.Name == "noisy-op" {
			t.Fatalf("noisy tenant span must be dropped; got %v", s.Name)
		}
	}
}

func TestTenantSampler_ParentBasedRespectsUpstreamDecision(t *testing.T) {
	// 0% everywhere — but an already-sampled parent span should
	// still cause downstream spans to be sampled (distributed
	// trace consistency).
	s, err := newTenantSampler(0, map[string]float64{"alice": 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(s),
		sdktrace.WithSyncer(exporter),
	)
	tr := tp.Tracer("test")

	// Fabricate an "upstream sampled" trace context.
	parentSpanCtx := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		SpanID:     trace.SpanID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	parentCtx := trace.ContextWithSpanContext(context.Background(), parentSpanCtx)
	parentCtx, err = tenant.WithID(parentCtx, tenant.ID("alice"))
	if err != nil {
		t.Fatalf("tenant.WithID: %v", err)
	}

	_, child := tr.Start(parentCtx, "child-of-upstream")
	child.End()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("upstream-sampled trace must keep being sampled regardless of tenant override; got %d spans", len(spans))
	}
}

func TestTenantSampler_Description_ListsTenantsSorted(t *testing.T) {
	s, err := newTenantSampler(0.05, map[string]float64{
		"zulu":  1,
		"alpha": 0.5,
		"mike":  0.1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	desc := s.Description()
	alphaIdx := strings.Index(desc, "alpha")
	mikeIdx := strings.Index(desc, "mike")
	zuluIdx := strings.Index(desc, "zulu")
	if alphaIdx < 0 || mikeIdx < 0 || zuluIdx < 0 {
		t.Fatalf("description missing tenant id(s): %q", desc)
	}
	if alphaIdx >= mikeIdx || mikeIdx >= zuluIdx {
		t.Fatalf("description tenants must appear in sorted order to keep observability output stable; got %q", desc)
	}
}

func TestConfig_Validate_RejectsBadTenantOverrides(t *testing.T) {
	tests := []struct {
		name string
		cfg  Config
	}{
		{
			name: "empty tenant ID",
			cfg: Config{
				ServiceName: "svc", Endpoint: "h:4317",
				TenantSampleRates: map[string]float64{"": 0.1},
			},
		},
		{
			name: "rate below 0",
			cfg: Config{
				ServiceName: "svc", Endpoint: "h:4317",
				TenantSampleRates: map[string]float64{"a": -0.01},
			},
		},
		{
			name: "rate above 1",
			cfg: Config{
				ServiceName: "svc", Endpoint: "h:4317",
				TenantSampleRates: map[string]float64{"a": 1.01},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.cfg.Validate(); err == nil {
				t.Fatal("Validate must reject this configuration")
			}
		})
	}
}

func TestConfig_Validate_AllowsValidTenantOverrides(t *testing.T) {
	cfg := Config{
		ServiceName: "svc",
		Endpoint:    "h:4317",
		SampleRate:  0.5,
		TenantSampleRates: map[string]float64{
			"alice": 0.0,
			"bob":   1.0,
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid configuration; got %v", err)
	}
}
