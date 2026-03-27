package gormmysql

import (
	"crypto/tls"
	"log/slog"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/infra/sqldb"
)

// Driver implements gormdb.DriverConfig for MySQL/MariaDB. It wraps a
// MySQLConfig and delegates connection opening to [New].
//
// Usage:
//
//	driver := gormmysql.Driver{Config: sqldb.MySQLConfig{
//	    Host: "replica.db", Port: 3306, User: "app", Password: "secret", Name: "mydb",
//	}}
type Driver struct {
	Config sqldb.MySQLConfig
}

// DriverName returns "mysql".
func (d Driver) DriverName() string { return "mysql" }

// Open creates a GORM database connection to MySQL/MariaDB.
func (d Driver) Open(pool sqldb.PoolConfig, logger *slog.Logger, clientTLS *tls.Config) (*gorm.DB, error) {
	var opts []Option
	if clientTLS != nil {
		opts = append(opts, WithTLS(clientTLS))
	}
	return New(d.Config, pool, logger, opts...)
}
