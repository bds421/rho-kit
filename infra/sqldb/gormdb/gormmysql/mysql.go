// Package gormmysql provides a GORM connection factory for MySQL/MariaDB
// using the go-sql-driver/mysql driver. Import this package only when your
// service uses MySQL or MariaDB — it does not pull in PostgreSQL dependencies.
package gormmysql

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/bds421/rho-kit/infra/sqldb"
)

// tlsConfigCounter generates unique TLS config names to prevent global
// map overwrite when multiple connections use different TLS configs.
var tlsConfigCounter atomic.Uint64

// Option configures the MySQL connection.
type Option func(*dbOpts)

type dbOpts struct {
	tlsConfig *tls.Config
}

// WithTLS enables mTLS for the MySQL/MariaDB connection.
func WithTLS(cfg *tls.Config) Option {
	return func(o *dbOpts) { o.tlsConfig = cfg }
}

// New opens a GORM database connection to MySQL/MariaDB with connection pooling.
//
// Deprecated: Use [MySQLDriver.Open] with the unified [sqldb.Config] instead.
//
//nolint:staticcheck // Uses deprecated MySQLConfig for backward compat.
func New(cfg sqldb.MySQLConfig, poolCfg sqldb.PoolConfig, logger *slog.Logger, opts ...Option) (*gorm.DB, error) {
	var o dbOpts
	for _, opt := range opts {
		opt(&o)
	}

	tlsEnabled := false
	tlsKey := "custom"
	if o.tlsConfig != nil {
		tlsKey = fmt.Sprintf("custom-%d", tlsConfigCounter.Add(1))
		if err := mysqldriver.RegisterTLSConfig(tlsKey, o.tlsConfig); err != nil {
			return nil, fmt.Errorf("register mysql TLS config: %w", err)
		}
		tlsEnabled = true
		logger.Info("database TLS enabled")
	}

	logLevel := gormlogger.Warn
	if cfg.LogLevel == "info" {
		logLevel = gormlogger.Info
	}

	var dsn string
	if tlsEnabled {
		dsn = cfg.DSN(tlsKey)
	} else {
		dsn = cfg.DSN()
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(logLevel),
	})
	if err != nil {
		logger.Warn("database connection failed", "host", cfg.Host, "name", cfg.Name, "error", err)
		return nil, fmt.Errorf("connect to database %s@%s:%d/%s: connection failed", cfg.User, cfg.Host, cfg.Port, cfg.Name)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get sql.DB instance: %w", err)
	}

	sqlDB.SetMaxIdleConns(poolCfg.MaxIdleConns)
	sqlDB.SetMaxOpenConns(poolCfg.MaxOpenConns)
	sqlDB.SetConnMaxLifetime(poolCfg.ConnMaxLifetime)
	sqlDB.SetConnMaxIdleTime(poolCfg.ConnMaxIdleTime)

	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	logger.Info("database connected", "host", cfg.Host, "name", cfg.Name)

	return db, nil
}
