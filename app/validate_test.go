package app

import (
	"io/fs"
	"log/slog"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormmysql"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormpostgres"
	pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx"
)

// newTestBuilder constructs a Builder with the always-on
// production-safety validator's TLS / audience checks opted out so
// individual tests can focus on the configuration knob they care
// about. Tests that exercise TLS or audience enforcement build their
// own Builder without these opt-outs.
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

func TestValidate_DatabaseWithoutPool(t *testing.T) {
	b := newTestBuilder()
	b.dbDriver = gormmysql.MySQLDriver{}
	b.dbCfg = &sqldb.Config{Host: "localhost"}
	if err := b.Validate(); err == nil {
		t.Fatal("expected error for database without pool")
	}
}

// Note: dual database (MySQL + Postgres) is no longer possible since both
// use the same dbDriver field. The mutex exclusion is handled by panics in
// WithMySQL/WithPostgres.

func TestValidate_DatabaseWithPool(t *testing.T) {
	b := newTestBuilder()
	b.dbDriver = gormpostgres.PostgresDriver{}
	// Postgres requires an explicit TLS-enforcing sslmode under the
	// always-on validator; this test only cares that pool + driver pass.
	b.dbCfg = &sqldb.Config{Host: "localhost", Options: map[string]string{"sslmode": "require"}}
	b.dbPoolCfg = &sqldb.PoolConfig{}
	if err := b.Validate(); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidate_DatabaseMetricsWithoutDB(t *testing.T) {
	b := newTestBuilder()
	b.dbMetrics = true
	if err := b.Validate(); err == nil {
		t.Fatal("expected error for db metrics without database")
	}
}

func TestValidate_SeedWithoutDB(t *testing.T) {
	b := newTestBuilder()
	b.seedFn = func(_ *gorm.DB, _ string, _ *slog.Logger) error { return nil }
	if err := b.Validate(); err == nil {
		t.Fatal("expected error for seed without database")
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

func TestValidate_PgxWithMigrations(t *testing.T) {
	b := New("test-svc", "v0.1.0", validBaseConfig()).
		WithoutTLS().
		WithoutJWTAudience().
		WithPgx(pgxbackend.Config{DSN: "postgres://u:p@h/db?sslmode=require"}).
		WithMigrations(emptyFS{})
	if err := b.Validate(); err != nil {
		t.Fatalf("expected pgx + migrations to validate, got: %v", err)
	}
}

func TestValidate_MigrationsWithoutDriver(t *testing.T) {
	b := newTestBuilder().WithMigrations(emptyFS{})
	err := b.Validate()
	if err == nil {
		t.Fatal("expected error for migrations without a configured database")
	}
	if !strings.Contains(err.Error(), "WithPgx") {
		t.Fatalf("expected WithPgx mentioned in migrations error, got: %v", err)
	}
}

type emptyFS struct{}

func (emptyFS) Open(_ string) (fs.File, error) { return nil, fs.ErrNotExist }
