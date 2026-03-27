package gormdb

import (
	"fmt"
	"log/slog"

	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"

	"github.com/bds421/rho-kit/infra/sqldb"
)

// ReplicaConfig holds read replica connection settings.
// Exactly one of MySQL or Postgres must be set; the driver must match
// the primary database's driver.
type ReplicaConfig struct {
	MySQL    *sqldb.MySQLConfig
	Postgres *sqldb.PostgresConfig
	Pool     sqldb.PoolConfig
}

// Validate checks that the ReplicaConfig has exactly one driver configured.
func (c ReplicaConfig) Validate() error {
	if c.MySQL == nil && c.Postgres == nil {
		return fmt.Errorf("gormdb: replica config requires MySQL or Postgres")
	}
	if c.MySQL != nil && c.Postgres != nil {
		return fmt.Errorf("gormdb: replica config must specify MySQL or Postgres, not both")
	}
	return nil
}

// Driver returns "mysql" or "postgres" based on which config is set.
func (c ReplicaConfig) Driver() string {
	if c.MySQL != nil {
		return "mysql"
	}
	return "postgres"
}

// RegisterReplica configures GORM's DBResolver plugin on the primary DB so
// that read queries are routed to the replica and writes/transactions stay
// on the primary.
//
// The caller is responsible for opening the replica *gorm.DB (via gormmysql
// or gormpostgres) and for closing it during shutdown. This function only
// registers the replica's Dialector with the DBResolver plugin.
//
// Returns an error if primary or replica is nil, or if the DBResolver plugin
// fails to register.
func RegisterReplica(primary, replica *gorm.DB, logger *slog.Logger) error {
	if primary == nil {
		return fmt.Errorf("gormdb: primary database must not be nil")
	}
	if replica == nil {
		return fmt.Errorf("gormdb: replica database must not be nil")
	}

	if err := primary.Use(dbresolver.Register(dbresolver.Config{
		Replicas: []gorm.Dialector{replica.Dialector},
		Policy:   dbresolver.RandomPolicy{},
	})); err != nil {
		return fmt.Errorf("gormdb: register db resolver: %w", err)
	}

	logger.Info("read replica registered via DBResolver")
	return nil
}

// CloseDB closes the underlying sql.DB connection pool of a *gorm.DB.
// It is safe to call with a nil db.
func CloseDB(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("gormdb: get underlying sql.DB: %w", err)
	}
	return sqlDB.Close()
}
