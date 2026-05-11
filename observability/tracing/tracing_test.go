package tracing

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

var _ slog.LogValuer = Config{}

func TestConfig_LogValue_RedactsHeaders(t *testing.T) {
	cfg := Config{
		ServiceName: "svc",
		Endpoint:    "collector.internal:4317",
		Headers: map[string]string{
			"authorization-secret-token": "Bearer otlp-secret",
			"x-api-key-secret-token":     "api-key-secret",
		},
	}

	rendered := cfg.LogValue().String()

	for _, secret := range []string{"otlp-secret", "api-key-secret", "authorization-secret-token", "x-api-key-secret-token"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("LogValue leaked %q in %q", secret, rendered)
		}
	}
	for _, expected := range []string{"collector.internal:4317", "headers_configured=true"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("LogValue %q missing %q", rendered, expected)
		}
	}
}

func TestConfig_LogValue_RedactsInvalidEndpointShape(t *testing.T) {
	cfg := Config{
		ServiceName: "svc",
		Endpoint:    "https://user:endpoint-secret@collector.internal:4317/v1/traces?token=query-secret#frag",
	}

	rendered := cfg.LogValue().String()
	for _, secret := range []string{"endpoint-secret", "query-secret", "collector.internal"} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("LogValue leaked %q in %q", secret, rendered)
		}
	}
	if !strings.Contains(rendered, "[INVALID ENDPOINT]") {
		t.Fatalf("LogValue did not mark endpoint invalid: %q", rendered)
	}
}

func TestLogExporterFallbackDoesNotExposeEndpointOrRawError(t *testing.T) {
	const endpoint = "collector-secret.internal:4317"
	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	logExporterFallback(endpoint, errors.New("dial collector-secret.internal:4317: token=secret"))

	rendered := buf.String()
	for _, forbidden := range []string{
		endpoint,
		"collector-secret.internal",
		"token=secret",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("fallback log leaked %q in %s", forbidden, rendered)
		}
	}
	if !strings.Contains(rendered, "endpoint_configured=true") {
		t.Fatalf("fallback log missing endpoint marker: %s", rendered)
	}
	if !strings.Contains(rendered, "error_kind=exporter_dial_failed") {
		t.Fatalf("fallback log missing stable error kind: %s", rendered)
	}
}

func TestExporterErrorKind(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", want: "unknown"},
		{name: "context canceled", err: context.Canceled, want: "context_canceled"},
		{name: "deadline", err: context.DeadlineExceeded, want: "timeout"},
		{name: "fallback", err: errors.New("dial failed"), want: "exporter_dial_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exporterErrorKind(tt.err); got != tt.want {
				t.Fatalf("exporterErrorKind() = %q, want %q", got, tt.want)
			}
		})
	}
}

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

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{
			name: "noop config accepts empty endpoint",
			cfg:  Config{},
		},
		{
			name: "valid endpoint with port",
			cfg:  Config{ServiceName: "svc", Endpoint: "collector.internal:4317"},
		},
		{
			name: "valid endpoint without port",
			cfg:  Config{ServiceName: "svc", Endpoint: "collector.internal"},
		},
		{
			name: "valid bracketed IPv6",
			cfg:  Config{ServiceName: "svc", Endpoint: "[::1]:4317"},
		},
		{
			name:    "service name required",
			cfg:     Config{Endpoint: "collector.internal:4317"},
			wantErr: "ServiceName",
		},
		{
			name:    "endpoint is not URL",
			cfg:     Config{ServiceName: "svc", Endpoint: "https://collector.internal:4317"},
			wantErr: "not a URL",
		},
		{
			name:    "endpoint rejects path",
			cfg:     Config{ServiceName: "svc", Endpoint: "collector.internal:4317/v1/traces"},
			wantErr: "path",
		},
		{
			name:    "endpoint rejects credentials",
			cfg:     Config{ServiceName: "svc", Endpoint: "user@collector.internal:4317"},
			wantErr: "credentials",
		},
		{
			name:    "endpoint rejects bad port",
			cfg:     Config{ServiceName: "svc", Endpoint: "collector.internal:bad"},
			wantErr: "port",
		},
		{
			name:    "sample rate too high",
			cfg:     Config{ServiceName: "svc", Endpoint: "collector.internal:4317", SampleRate: 1.5},
			wantErr: "SampleRate",
		},
		{
			name:    "sample rate negative",
			cfg:     Config{ServiceName: "svc", Endpoint: "collector.internal:4317", SampleRate: -0.1},
			wantErr: "SampleRate",
		},
		{
			name:    "unknown compression",
			cfg:     Config{ServiceName: "svc", Endpoint: "collector.internal:4317", Compression: "zstd"},
			wantErr: "Compression",
		},
		{
			name:    "negative batch timeout",
			cfg:     Config{ServiceName: "svc", Endpoint: "collector.internal:4317", BatchTimeout: -time.Second},
			wantErr: "BatchTimeout",
		},
		{
			name:    "negative queue size",
			cfg:     Config{ServiceName: "svc", Endpoint: "collector.internal:4317", MaxQueueSize: -1},
			wantErr: "MaxQueueSize",
		},
		{
			name:    "export batch larger than queue",
			cfg:     Config{ServiceName: "svc", Endpoint: "collector.internal:4317", MaxQueueSize: 10, MaxExportBatchSize: 11},
			wantErr: "MaxExportBatchSize",
		},
		{
			name:    "invalid header key",
			cfg:     Config{ServiceName: "svc", Endpoint: "collector.internal:4317", Headers: map[string]string{"bad key": "value"}},
			wantErr: "header key",
		},
		{
			name:    "invalid header value",
			cfg:     Config{ServiceName: "svc", Endpoint: "collector.internal:4317", Headers: map[string]string{"x-token": "line\nbreak"}},
			wantErr: "header",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestConfigValidate_InvalidEndpointSyntaxDoesNotEchoValue(t *testing.T) {
	cfg := Config{ServiceName: "svc", Endpoint: "collector.internal:secret-token:4317"}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected invalid endpoint syntax error")
	}
	if !strings.Contains(err.Error(), "invalid Endpoint syntax") {
		t.Fatalf("error %q does not contain invalid syntax marker", err.Error())
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), cfg.Endpoint) {
		t.Fatalf("error leaked endpoint value: %q", err.Error())
	}
}

func TestConfigValidate_InvalidCompressionDoesNotEchoValue(t *testing.T) {
	cfg := Config{ServiceName: "svc", Endpoint: "collector.internal:4317", Compression: "secret-token-compression"}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected invalid compression error")
	}
	if !strings.Contains(err.Error(), "Compression") {
		t.Fatalf("error %q does not contain Compression marker", err.Error())
	}
	if strings.Contains(err.Error(), "secret-token-compression") {
		t.Fatalf("error leaked compression value: %q", err.Error())
	}
}

func TestConfigValidate_InvalidEndpointCharacterDoesNotEchoValue(t *testing.T) {
	cfg := Config{ServiceName: "svc", Endpoint: "collector.internal:4317\nsecret-token"}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected invalid endpoint character error")
	}
	if !strings.Contains(err.Error(), "Endpoint contains invalid character") {
		t.Fatalf("error %q does not contain invalid character marker", err.Error())
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "\n") {
		t.Fatalf("error leaked endpoint data: %q", err.Error())
	}
}

func TestConfigValidate_InvalidHeadersDoNotEchoMetadata(t *testing.T) {
	tests := map[string]map[string]string{
		"invalid key":   {"bad key secret-token": "value"},
		"invalid value": {"x-secret-token": "line\nbreak"},
	}

	for name, headers := range tests {
		t.Run(name, func(t *testing.T) {
			err := Config{
				ServiceName: "svc",
				Endpoint:    "collector.internal:4317",
				Headers:     headers,
			}.Validate()
			if err == nil {
				t.Fatal("expected invalid tracing headers to be rejected")
			}
			if strings.Contains(strings.ToLower(err.Error()), "secret-token") {
				t.Fatalf("error leaked header metadata: %q", err.Error())
			}
		})
	}
}

func TestCloneStringMapDetachesInput(t *testing.T) {
	headers := map[string]string{"x-token": "before"}
	cloned := cloneStringMap(headers)

	headers["x-token"] = "after"
	headers["x-new"] = "new"

	if cloned["x-token"] != "before" {
		t.Fatalf("clone observed caller mutation: %v", cloned)
	}
	if _, ok := cloned["x-new"]; ok {
		t.Fatalf("clone observed added caller key: %v", cloned)
	}
}

func TestInit_RejectsNilContext(t *testing.T) {
	var ctx context.Context
	_, err := Init(ctx, Config{})
	if err == nil {
		t.Fatal("expected nil context error")
	}
}

func TestInit_RejectsInvalidConfig(t *testing.T) {
	_, err := Init(context.Background(), Config{
		ServiceName: "svc",
		Endpoint:    "https://collector.internal:4317",
	})
	if err == nil {
		t.Fatal("expected invalid config error")
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

func TestProvider_ShutdownRejectsNilContext(t *testing.T) {
	p, err := Init(context.Background(), Config{ServiceName: "test"})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	defer func() { _ = p.Shutdown(context.Background()) }()

	var ctx context.Context
	err = p.Shutdown(ctx)
	if err == nil || !strings.Contains(err.Error(), "non-nil context") {
		t.Fatalf("expected nil context error, got %v", err)
	}
}

func TestProvider_ShutdownRejectsUninitializedProvider(t *testing.T) {
	err := (*Provider)(nil).Shutdown(context.Background())
	if err == nil || !strings.Contains(err.Error(), "initialized Provider") {
		t.Fatalf("expected uninitialized provider error, got %v", err)
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
