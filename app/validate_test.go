package app

import (
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/bds421/rho-kit/security/v2/netutil"
)

var _ slog.LogValuer = BaseConfig{}

// newTestBuilder constructs a Builder with the always-on
// production-safety validator's TLS / audience / rate-limit checks
// opted out so individual tests can focus on the configuration knob
// they care about.
func newTestBuilder() *Builder {
	return New("test-svc", "v0.1.0", validBaseConfig()).
		WithoutTLS().
		WithoutRateLimit()
}

// validBaseConfig returns a BaseConfig with valid server / internal
// ports. ValidateBase rejects the zero-value defaults, so tests that
// run Validate() must seed both fields.
func validBaseConfig() BaseConfig {
	return BaseConfig{
		Server:   ServerConfig{Port: 8080},
		Internal: InternalConfig{Port: 9090},
	}
}

func TestBaseConfigLogValueRedactsTLSPaths(t *testing.T) {
	cfg := BaseConfig{
		Server:      ServerConfig{Host: "0.0.0.0", Port: 8080},
		Internal:    InternalConfig{Host: "127.0.0.1", Port: 9090},
		Environment: "production",
		LogLevel:    "info",
		TLS: netutil.TLSConfig{
			CACert: "/var/run/secrets/tls/ca.pem",
			Cert:   "/var/run/secrets/tls/cert.pem",
			Key:    "/var/run/secrets/tls/key.pem",
		},
	}

	rendered := cfg.LogValue().String()

	for _, path := range []string{cfg.TLS.CACert, cfg.TLS.Cert, cfg.TLS.Key} {
		if strings.Contains(rendered, path) {
			t.Fatalf("LogValue leaked TLS path %q in %q", path, rendered)
		}
	}
	for _, expected := range []string{"0.0.0.0:8080", "127.0.0.1:9090", "key_configured=true"} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("LogValue %q missing %q", rendered, expected)
		}
	}
}

func TestValidate_NilBuilder(t *testing.T) {
	var b *Builder
	if err := b.Validate(); err == nil {
		t.Fatal("expected error for nil builder")
	}
}

func TestValidate_EmptyBuilder(t *testing.T) {
	b := newTestBuilder()
	if err := b.Validate(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidate_RateLimitRequestsWithoutWindow(t *testing.T) {
	b := newTestBuilder()
	b.ipRateRequests = 100
	b.ipRateWindow = 0
	if err := b.Validate(); err == nil {
		t.Fatal("expected error for rate limit requests without window")
	}
}

func TestValidate_RateLimitWindowWithoutRequests(t *testing.T) {
	b := newTestBuilder()
	b.ipRateRequests = 0
	b.ipRateWindow = time.Second
	if err := b.Validate(); err == nil {
		t.Fatal("expected error for rate limit window without requests")
	}
}

func TestValidate_RateLimitValid(t *testing.T) {
	b := newTestBuilder()
	b.ipRateRequests = 100
	b.ipRateWindow = time.Minute
	if err := b.Validate(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidate_KeyedLimiterEmptyName(t *testing.T) {
	b := newTestBuilder()
	b.keyedLimiters = []keyedLimiterSpec{{name: "", requests: 10, window: time.Second}}
	if err := b.Validate(); err == nil {
		t.Fatal("expected error for empty keyed limiter name")
	}
}

func TestValidate_KeyedLimiterZeroRequests(t *testing.T) {
	b := newTestBuilder()
	b.keyedLimiters = []keyedLimiterSpec{{name: "api", requests: 0, window: time.Second}}
	if err := b.Validate(); err == nil {
		t.Fatal("expected error for zero keyed limiter requests")
	}
}

func TestValidate_KeyedLimiterZeroWindow(t *testing.T) {
	b := newTestBuilder()
	b.keyedLimiters = []keyedLimiterSpec{{name: "api", requests: 10, window: 0}}
	if err := b.Validate(); err == nil {
		t.Fatal("expected error for zero keyed limiter window")
	}
}

func TestValidate_KeyedLimiterValid(t *testing.T) {
	b := newTestBuilder()
	b.keyedLimiters = []keyedLimiterSpec{{name: "api", requests: 10, window: time.Second}}
	if err := b.Validate(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidate_InvalidServerPort(t *testing.T) {
	cfg := validBaseConfig()
	cfg.Server.Port = 0
	b := New("test-svc", "v0.1.0", cfg).
		WithoutTLS().
		WithoutRateLimit()
	err := b.Validate()
	if err == nil {
		t.Fatal("expected error for invalid server port")
	}
	if !strings.Contains(err.Error(), "server") {
		t.Fatalf("expected server-port error, got: %v", err)
	}
}

func TestValidate_InvalidInternalPort(t *testing.T) {
	cfg := validBaseConfig()
	cfg.Internal.Port = 70000
	b := New("test-svc", "v0.1.0", cfg).
		WithoutTLS().
		WithoutRateLimit()
	err := b.Validate()
	if err == nil {
		t.Fatal("expected error for invalid internal port")
	}
	if !strings.Contains(err.Error(), "internal") {
		t.Fatalf("expected internal-port error, got: %v", err)
	}
}

func TestValidate_ValidPorts(t *testing.T) {
	cfg := validBaseConfig()
	cfg.Server.Port = 1
	cfg.Internal.Port = 65535
	b := New("test-svc", "v0.1.0", cfg).
		WithoutTLS().
		WithoutRateLimit()
	if err := b.Validate(); err != nil {
		t.Fatalf("expected no error for valid ports, got: %v", err)
	}
}

// TestValidate_NoRateLimitDeclarationRejected pins Lens F A.5: a Builder
// that declares no rate limiter at all must fail at Validate() with an
// actionable error naming all three options.
func TestValidate_NoRateLimitDeclarationRejected(t *testing.T) {
	b := New("test-svc", "v0.1.0", validBaseConfig()).
		WithoutTLS()
	err := b.Validate()
	if err == nil {
		t.Fatal("expected error when no rate-limit declaration is present")
	}
	for _, want := range []string{"WithIPRateLimit", "WithKeyedRateLimit", "WithoutRateLimit"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("rate-limit error must name %q (got: %v)", want, err)
		}
	}
}

// TestValidate_WithIPRateLimitSatisfiesGate confirms WithIPRateLimit is
// one of the three accepted declarations.
func TestValidate_WithIPRateLimitSatisfiesGate(t *testing.T) {
	b := New("test-svc", "v0.1.0", validBaseConfig()).
		WithoutTLS().
		WithIPRateLimit(100, time.Minute)
	if err := b.Validate(); err != nil {
		t.Fatalf("WithIPRateLimit must satisfy the rate-limit gate, got: %v", err)
	}
}

// TestValidate_WithKeyedRateLimitSatisfiesGate confirms a single keyed
// limiter is sufficient — services may rate-limit by API key alone.
func TestValidate_WithKeyedRateLimitSatisfiesGate(t *testing.T) {
	b := New("test-svc", "v0.1.0", validBaseConfig()).
		WithoutTLS().
		WithKeyedRateLimit("api", 10, time.Minute)
	if err := b.Validate(); err != nil {
		t.Fatalf("WithKeyedRateLimit must satisfy the rate-limit gate, got: %v", err)
	}
}

// TestValidate_WithoutRateLimitSatisfiesGate confirms the explicit
// opt-out is honored for traffic-bounded services.
func TestValidate_WithoutRateLimitSatisfiesGate(t *testing.T) {
	b := New("test-svc", "v0.1.0", validBaseConfig()).
		WithoutTLS().
		WithoutRateLimit()
	if err := b.Validate(); err != nil {
		t.Fatalf("WithoutRateLimit must satisfy the rate-limit gate, got: %v", err)
	}
}
