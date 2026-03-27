package gormpostgres

import (
	"crypto/tls"
	"log/slog"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/infra/sqldb"
)

// Driver implements gormdb.DriverConfig for PostgreSQL. It wraps a
// PostgresConfig and delegates connection opening to [New].
//
// Usage:
//
//	driver := gormpostgres.Driver{Config: sqldb.PostgresConfig{
//	    Host: "replica.db", Port: 5432, User: "app", Password: "secret", Name: "mydb",
//	}}
type Driver struct {
	Config sqldb.PostgresConfig
}

// DriverName returns "postgres".
func (d Driver) DriverName() string { return "postgres" }

// Open creates a GORM database connection to PostgreSQL.
func (d Driver) Open(pool sqldb.PoolConfig, logger *slog.Logger, clientTLS *tls.Config) (*gorm.DB, error) {
	var opts []Option
	if clientTLS != nil {
		opts = append(opts, WithTLS(clientTLS))
	}
	return New(d.Config, pool, logger, opts...)
}
