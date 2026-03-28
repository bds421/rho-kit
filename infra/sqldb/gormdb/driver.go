package gormdb

import (
	"crypto/tls"
	"log/slog"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/infra/sqldb"
)

// DriverConfig abstracts database driver configuration for GORM connections.
// Implementations exist for MySQL (gormmysql.Driver) and PostgreSQL
// (gormpostgres.Driver). This interface eliminates MySQL/Postgres branching
// in code that opens database connections -- callers use a single code path
// regardless of the underlying database engine.
type DriverConfig interface {
	// DriverName returns the database driver name (e.g. "mysql", "postgres").
	DriverName() string

	// Open creates a GORM database connection with the given pool settings,
	// logger, and optional TLS configuration. Implementations handle
	// driver-specific DSN construction, dialector creation, and connection
	// pooling.
	Open(pool sqldb.PoolConfig, logger *slog.Logger, clientTLS *tls.Config) (*gorm.DB, error)
}
