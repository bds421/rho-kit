package gormmysql

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/url"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
)

// Compile-time interface check.
var _ gormdb.Driver = (*MySQLDriver)(nil)

// MySQLDriver implements [gormdb.Driver] for MySQL/MariaDB. It constructs the
// DSN from the unified [sqldb.Config] and opens a GORM connection using the
// go-sql-driver/mysql driver.
type MySQLDriver struct{}

// Name returns "mysql".
func (*MySQLDriver) Name() string { return "mysql" }

// Open creates a GORM database connection to MySQL/MariaDB using the unified
// config. It registers TLS when clientTLS is non-nil and applies connection
// pool settings.
func (*MySQLDriver) Open(
	cfg sqldb.Config,
	pool sqldb.PoolConfig,
	logger *slog.Logger,
	clientTLS *tls.Config,
) (*gorm.DB, error) {
	dsn := buildMySQLDSN(cfg)

	if clientTLS != nil {
		tlsKey := fmt.Sprintf("custom-%d", tlsConfigCounter.Add(1))
		if err := mysqldriver.RegisterTLSConfig(tlsKey, clientTLS); err != nil {
			return nil, fmt.Errorf("register mysql TLS config: %w", err)
		}
		dsn += "&tls=" + url.QueryEscape(tlsKey)
		logger.Info("database TLS enabled")
	}

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

	return db, nil
}

// buildMySQLDSN constructs a MySQL DSN from the unified Config.
func buildMySQLDSN(cfg sqldb.Config) string {
	charset := cfg.Option("charset", "utf8mb4")
	return fmt.Sprintf(
		"%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=True&loc=Local&clientFoundRows=true",
		url.QueryEscape(cfg.User),
		url.QueryEscape(cfg.Password),
		cfg.Host,
		cfg.Port,
		url.QueryEscape(cfg.Name),
		url.QueryEscape(charset),
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
