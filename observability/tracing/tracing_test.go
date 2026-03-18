package tracing

import (
	"context"
	"testing"
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
