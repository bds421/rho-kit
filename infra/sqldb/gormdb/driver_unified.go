package gormdb

import (
	"crypto/tls"
	"log/slog"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/infra/sqldb"
)

// Driver abstracts the database engine so callers can open connections
// without importing a specific driver package. Unlike [DriverConfig], the
// connection configuration is passed to Open rather than bundled inside the
// implementation, making it composable with the unified [sqldb.Config].
//
// Implementations will be added to gormmysql and gormpostgres in a
// follow-up release. Until then, use [DriverConfig] which bundles the
// config inside the driver struct.
type Driver interface {
	// Name returns the driver identifier (e.g. "mysql", "postgres").
	Name() string

	// Open creates a GORM database connection using the given configuration,
	// pool settings, logger, and optional client TLS config. Pass nil for
	// clientTLS when TLS is not required.
	Open(cfg sqldb.Config, pool sqldb.PoolConfig, logger *slog.Logger, clientTLS *tls.Config) (*gorm.DB, error)
}
