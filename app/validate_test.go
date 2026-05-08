package app

import (
	"io/fs"
	"strings"
	"testing"
	"time"

	pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
)

// newTestBuilder constructs a Builder with the always-on
// production-safety validator's TLS / audience checks opted out so
// individual tests can focus on the configuration knob they care about.
func newTestBuilder() *Builder {
	return New("test-svc", "v0.1.0", validBaseConfig()).
		WithoutTLS().
		WithoutJWTAudience()
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

func TestValidate_CriticalBrokerWithoutURL(t *testing.T) {
	b := newTestBuilder()
	b.criticalBroker = true
	if err := b.Validate(); err == nil {
		t.Fatal("expected error for critical broker without URL")
	}
}

func TestValidate_CriticalBrokerWithURL(t *testing.T) {
	b := newTestBuilder()
	b.criticalBroker = true
	b.mqURL = "amqp://localhost"
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
		WithoutJWTAudience()
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
		WithoutJWTAudience()
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
		WithoutJWTAudience()
	if err := b.Validate(); err != nil {
		t.Fatalf("expected no error for valid ports, got: %v", err)
	}
}

func TestValidate_PostgresWithMigrations(t *testing.T) {
	b := New("test-svc", "v0.1.0", validBaseConfig()).
		WithoutTLS().
		WithoutJWTAudience().
		WithPostgres(pgxbackend.Config{DSN: "postgres://u:p@h/db?sslmode=require"}).
		WithMigrations(emptyFS{})
	if err := b.Validate(); err != nil {
		t.Fatalf("expected postgres + migrations to validate, got: %v", err)
	}
}

func TestValidate_MigrationsWithoutDB(t *testing.T) {
	b := newTestBuilder().WithMigrations(emptyFS{})
	err := b.Validate()
	if err == nil {
		t.Fatal("expected error for migrations without a configured database")
	}
	if !strings.Contains(err.Error(), "WithPostgres") {
		t.Fatalf("expected WithPostgres mentioned in migrations error, got: %v", err)
	}
}

type emptyFS struct{}

func (emptyFS) Open(_ string) (fs.File, error) { return nil, fs.ErrNotExist }
