package app

import (
	"log/slog"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormmysql"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormpostgres"
)

func newTestBuilder() *Builder {
	return New("test-svc", "v0.1.0", BaseConfig{})
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
	b.dbDriver = &gormmysql.MySQLDriver{}
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
	b.dbDriver = &gormpostgres.PostgresDriver{}
	b.dbCfg = &sqldb.Config{Host: "localhost"}
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
