package gormmysql

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
)

// Compile-time interface check.
var _ gormdb.Driver = MySQLDriver{}

// MySQLDriver implements [gormdb.Driver] for MySQL/MariaDB. It constructs the
// DSN from the unified [sqldb.Config] and opens a GORM connection using the
// go-sql-driver/mysql driver.
type MySQLDriver struct{}

// Name returns "mysql".
func (MySQLDriver) Name() string { return "mysql" }

// Open creates a GORM database connection to MySQL/MariaDB using the unified
// config. It registers TLS when clientTLS is non-nil OR when
// Config.Options["tls"] requests a registered config; in both cases the
// resulting DSN includes the corresponding tls= parameter.
//
// On every error path that occurs after a TLS registration the registration
// is released (refcount decremented) so failed opens do not leak global
// driver entries. Successful opens "commit" the registration: it stays
// alive for the lifetime of the connection and is released when the
// caller invokes [ReleaseTLS].
func (MySQLDriver) Open(
	cfg sqldb.Config,
	pool sqldb.PoolConfig,
	logger *slog.Logger,
	clientTLS *tls.Config,
) (*gorm.DB, error) {
	if logger == nil {
		logger = slog.Default()
	}
	dsn := buildMySQLDSN(cfg)

	tlsOpt := strings.ToLower(cfg.Option("tls", ""))
	var registeredTLS bool
	switch {
	case clientTLS != nil:
		tlsKey, err := registerTLSConfigDedup(clientTLS)
		if err != nil {
			return nil, fmt.Errorf("register mysql TLS config: %w", err)
		}
		registeredTLS = true
		dsn += "&tls=" + url.QueryEscape(tlsKey)
		logger.Info("database TLS enabled")
	case tlsOpt == "true":
		return nil, fmt.Errorf("mysql: Config.Options[\"tls\"]=true requires a registered *tls.Config; pass one via WithTLS or use a registered custom-* name")
	case tlsOpt == "" || tlsOpt == "false":
		// Plain TCP — DSN already complete.
	default:
		// Driver builtins ("skip-verify", "preferred") and caller-
		// registered "custom-*" names pass through verbatim.
		dsn += "&tls=" + url.QueryEscape(tlsOpt)
		logger.Info("database TLS enabled", "tls", tlsOpt)
	}

	committed := false
	defer func() {
		if registeredTLS && !committed {
			ReleaseTLS(clientTLS)
		}
	}()

	logLevel := gormlogger.Warn
	if cfg.LogLevel == "info" {
		logLevel = gormlogger.Info
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(logLevel),
	})
	if err != nil {
		logger.Warn("database connection failed",
			"host", cfg.Host, "name", cfg.Name, "error", err)
		return nil, fmt.Errorf("connect to database %s@%s:%d/%s: connection failed",
			cfg.User, cfg.Host, cfg.Port, cfg.Name)
	}

	if err := applyPool(db, pool); err != nil {
		return nil, err
	}

	if err := ping(db, logger, cfg); err != nil {
		return nil, err
	}

	committed = true
	return db, nil
}

// buildMySQLDSN constructs a MySQL DSN from the unified Config.
//
// Default loc is UTC (overridable via Config.Options["loc"]). The previous
// hard-coded loc=Local silently shifted timestamps based on the pod's local
// timezone — UTC pods read times as UTC; developer laptops read the same
// rows as local time. Standardising on UTC matches Postgres conventions and
// makes time-based comparisons across environments deterministic.
func buildMySQLDSN(cfg sqldb.Config) string {
	charset := cfg.Option("charset", "utf8mb4")
	loc := cfg.Option("loc", "UTC")
	return fmt.Sprintf(
		"%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=True&loc=%s&clientFoundRows=true",
		url.QueryEscape(cfg.User),
		url.QueryEscape(cfg.Password),
		cfg.Host,
		cfg.Port,
		url.QueryEscape(cfg.Name),
		url.QueryEscape(charset),
		url.QueryEscape(loc),
	)
}

// applyPool configures the underlying sql.DB connection pool.
func applyPool(db *gorm.DB, pool sqldb.PoolConfig) error {
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get sql.DB instance: %w", err)
	}
	sqlDB.SetMaxIdleConns(pool.MaxIdleConns)
	sqlDB.SetMaxOpenConns(pool.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(pool.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(pool.ConnMaxIdleTime)
	return nil
}

// ping verifies connectivity with a 5-second timeout.
func ping(db *gorm.DB, logger *slog.Logger, cfg sqldb.Config) error {
	if logger == nil {
		logger = slog.Default()
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get sql.DB for ping: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return fmt.Errorf("ping database: %w", err)
	}

	logger.Info("database connected", "host", cfg.Host, "name", cfg.Name)
	return nil
}
