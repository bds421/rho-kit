package gormdb

import (
	"crypto/tls"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/infra/sqldb"
)

// ReplicaConfig configures a read replica database connection.
// Use with [RegisterReplica] to set up read/write splitting.
type ReplicaConfig struct {
	// Driver provides the database-specific connection logic.
	// Use gormmysql.Driver{Config: ...} or gormpostgres.Driver{Config: ...}.
	Driver DriverConfig

	// Pool configures the replica's connection pool.
	Pool sqldb.PoolConfig
}

// Validate checks that the ReplicaConfig is properly configured.
func (c ReplicaConfig) Validate() error {
	if c.Driver == nil {
		return fmt.Errorf("replica config: Driver is required")
	}
	return nil
}

// RegisterReplica opens a read replica connection and returns it for use
// as a dedicated reader. The caller is responsible for closing the returned
// *gorm.DB via [CloseDB] during shutdown.
//
// The returned connection is a standalone *gorm.DB connected to the replica.
// It is NOT automatically registered with a DBResolver plugin -- the caller
// decides how to route queries (e.g. expose it as infra.DBReader for
// explicit read targeting).
func RegisterReplica(
	primary *gorm.DB,
	cfg ReplicaConfig,
	logger *slog.Logger,
	clientTLS *tls.Config,
) (*gorm.DB, error) {
	if primary == nil {
		return nil, fmt.Errorf("register replica: primary database is nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	logger.Info("connecting to read replica",
		"driver", cfg.Driver.DriverName())

	replicaDB, err := cfg.Driver.Open(cfg.Pool, logger, clientTLS)
	if err != nil {
		return nil, fmt.Errorf("open read replica (%s): %w",
			cfg.Driver.DriverName(), err)
	}

	return replicaDB, nil
}

// CloseDB closes the underlying sql.DB connection pool of a GORM database.
// Safe to call with a nil db.
func CloseDB(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("get sql.DB for close: %w", err)
	}
	return sqlDB.Close()
}
