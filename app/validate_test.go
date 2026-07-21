package app

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/bds421/rho-kit/security/v2/netutil"
)

var _ slog.LogValuer = BaseConfig{}

// newTestBuilder constructs a Builder with the always-on
// production-safety validator's TLS / audience / rate-limit checks
// opted out so individual tests can focus on the configuration knob
// they care about.
func newTestBuilder() *Builder {
	return New("test-svc", "v0.1.0", validBaseConfig()).
		With(allowPlaintextOnly()).
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

// Per-module rate-limit parameter validation (requests > 0,
// window > 0, name non-empty + metric-safe, duplicate dedupe)
// moved to app/ratelimit.IP / Keyed constructors —
// see their package tests.

func TestValidate_InvalidServerPort(t *testing.T) {
	cfg := validBaseConfig()
	cfg.Server.Port = 0
	b := New("test-svc", "v0.1.0", cfg).
		With(allowPlaintextOnly()).
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
		With(allowPlaintextOnly()).
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
		With(allowPlaintextOnly()).
		WithoutRateLimit()
	if err := b.Validate(); err != nil {
		t.Fatalf("expected no error for valid ports, got: %v", err)
	}
}

// TestValidate_NoRateLimitDeclarationRejected pins Lens F A.5: a Builder
// that declares no rate limiter at all must fail at Validate() with an
// actionable error naming IP and WithoutRateLimit.
func TestValidate_NoRateLimitDeclarationRejected(t *testing.T) {
	b := New("test-svc", "v0.1.0", validBaseConfig()).
		With(allowPlaintextOnly())
	err := b.Validate()
	if err == nil {
		t.Fatal("expected error when no rate-limit declaration is present")
	}
	for _, want := range []string{"ratelimit.IP", "WithoutRateLimit"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("rate-limit error must name %q (got: %v)", want, err)
		}
	}
}

// fakeRateLimitDeclarer implements RateLimitDeclarer so we can
// drive the gate test from core app without importing
// app/ratelimit (which would invert the dep direction).
type fakeRateLimitDeclarer struct{ BaseModule }

func (fakeRateLimitDeclarer) DeclaresRateLimit() {}

// TestValidate_RateLimitDeclarerSatisfiesGate confirms that any
// module implementing RateLimitDeclarer satisfies the gate — the
// real-world implementation (app/ratelimit.IP) gets its own tests
// in that package. Keyed deliberately does NOT declare rate limit.
func TestValidate_RateLimitDeclarerSatisfiesGate(t *testing.T) {
	b := New("test-svc", "v0.1.0", validBaseConfig()).
		With(allowPlaintextOnly()).
		With(fakeRateLimitDeclarer{BaseModule: NewBaseModule("fake-rl")})
	if err := b.Validate(); err != nil {
		t.Fatalf("RateLimitDeclarer module must satisfy the rate-limit gate, got: %v", err)
	}
}

// TestValidate_WithoutRateLimitSatisfiesGate confirms the explicit
// opt-out is honored for traffic-bounded services.
func TestValidate_WithoutRateLimitSatisfiesGate(t *testing.T) {
	b := New("test-svc", "v0.1.0", validBaseConfig()).
		With(allowPlaintextOnly()).
		WithoutRateLimit()
	if err := b.Validate(); err != nil {
		t.Fatalf("WithoutRateLimit must satisfy the rate-limit gate, got: %v", err)
	}
}
