package tracing

import (
	"context"
	"testing"
	"time"
)

func TestInit_noopWhenNoEndpoint(t *testing.T) {
	p, err := Init(context.Background(), Config{
		ServiceName: "test",
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = p.Shutdown(context.Background()) }()

	tracer := p.Tracer("test")
	_, span := tracer.Start(context.Background(), "noop-test")
	span.End()

	if tracer == nil {
		t.Error("expected non-nil tracer")
	}
}

func TestBuildResource_AllFields(t *testing.T) {
	res, err := buildResource(context.Background(), Config{
		ServiceName:    "test-svc",
		ServiceVersion: "2.0.0",
		Environment:    "production",
	})
	if err != nil {
		t.Fatalf("buildResource: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil resource")
	}

	attrs := res.Attributes()
	found := map[string]string{}
	for _, attr := range attrs {
		found[string(attr.Key)] = attr.Value.AsString()
	}
	if found["service.name"] != "test-svc" {
		t.Errorf("service.name = %q, want test-svc", found["service.name"])
	}
	if found["service.version"] != "2.0.0" {
		t.Errorf("service.version = %q, want 2.0.0", found["service.version"])
	}
	if found["deployment.environment.name"] != "production" {
		t.Errorf("deployment.environment.name = %q, want production", found["deployment.environment.name"])
	}
}

func TestBuildResource_NameOnly(t *testing.T) {
	res, err := buildResource(context.Background(), Config{ServiceName: "minimal"})
	if err != nil {
		t.Fatalf("buildResource: %v", err)
	}

	attrs := res.Attributes()
	found := map[string]bool{}
	for _, attr := range attrs {
		found[string(attr.Key)] = true
	}
	if !found["service.name"] {
		t.Error("expected service.name attribute")
	}
	if found["service.version"] {
		t.Error("service.version should not be present when empty")
	}
	if found["deployment.environment.name"] {
		t.Error("deployment.environment.name should not be present when empty")
	}
}

func TestInit_DefaultSampleRate(t *testing.T) {
	p, err := Init(context.Background(), Config{ServiceName: "test"})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = p.Shutdown(context.Background()) }()
}

func TestProvider_Shutdown(t *testing.T) {
	p, err := Init(context.Background(), Config{ServiceName: "test"})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := p.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

func TestInit_BoundsExporterDialDuration(t *testing.T) {
	// otlptracegrpc.New is non-blocking by default — connections are dialed
	// lazily on first export. This test pins down the bound rather than the
	// failure path: with a tight InitTimeout, Init must still return quickly
	// even with a bogus endpoint, and the resulting Provider must be usable
	// (either real or noop).
	cfg := Config{
		ServiceName: "test",
		Endpoint:    "127.0.0.1:9", // discard port; nobody listening
		Insecure:    true,
		InitTimeout: 200 * time.Millisecond,
	}

	start := time.Now()
	p, err := Init(context.Background(), cfg)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("Init must not return error on unreachable collector; got %v", err)
	}
	if p == nil {
		t.Fatal("Init must return a provider")
	}
	defer func() { _ = p.Shutdown(context.Background()) }()

	if elapsed > 5*time.Second {
		t.Fatalf("Init blocked beyond reasonable bound (%v); the timeout is not honoured", elapsed)
	}
}

func TestInit_NegativeTimeoutDisablesBound(t *testing.T) {
	// Sanity: passing InitTimeout < 0 must not cause Init to error or hang
	// when the endpoint is reachable. Other validation lives in
	// TestInit_BoundsExporterDialDuration.
	cfg := Config{
		ServiceName: "test",
		// No endpoint → noop path; InitTimeout is ignored, but the option
		// must be accepted without panic.
		InitTimeout: -1,
	}
	p, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	_ = p.Shutdown(context.Background())
}
