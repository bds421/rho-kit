package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"

	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormmysql"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormpostgres"
)

// readReplicaModule implements the Module interface for database read replicas.
// It registers the GORM DBResolver plugin on the primary database connection,
// routing reads to the replica and writes/transactions to the primary.
//
// This module depends on the "database" module being initialized first (it
// retrieves the primary *gorm.DB via ModuleContext.Module("database")).
type readReplicaModule struct {
	BaseModule

	// config -- set at construction time, never mutated after.
	mysqlCfg *sqldb.MySQLConfig
	pgCfg    *sqldb.PostgresConfig
	poolCfg  sqldb.PoolConfig

	// initialized during Init
	replicaDB *gorm.DB
	logger    *slog.Logger
}

// readReplicaModuleConfig holds the configuration for constructing a readReplicaModule.
type readReplicaModuleConfig struct {
	mysqlCfg *sqldb.MySQLConfig
	pgCfg    *sqldb.PostgresConfig
	poolCfg  sqldb.PoolConfig
}

// newReadReplicaModule creates a read replica module from the given config.
// Panics if neither MySQL nor Postgres config is set.
func newReadReplicaModule(cfg readReplicaModuleConfig) *readReplicaModule {
	if cfg.mysqlCfg == nil && cfg.pgCfg == nil {
		panic("app: read replica module requires MySQL or Postgres config")
	}
	return &readReplicaModule{
		BaseModule: NewBaseModule("read-replica"),
		mysqlCfg:   cfg.mysqlCfg,
		pgCfg:      cfg.pgCfg,
		poolCfg:    cfg.poolCfg,
	}
}

func (m *readReplicaModule) Init(_ context.Context, mc ModuleContext) error {
	m.logger = mc.Logger

	dbMod, ok := mc.Module("database").(*databaseModule)
	if !ok {
		return fmt.Errorf("read replica module: \"database\" module is not a *databaseModule (check registration order)")
	}
	primaryDB := dbMod.DB()
	if primaryDB == nil {
		return fmt.Errorf("read replica module: primary database not initialized")
	}

	clientTLS, err := mc.Config.TLS.ClientTLS()
	if err != nil {
		return fmt.Errorf("read replica module: build client TLS: %w", err)
	}

	replicaDB, openErr := m.openReplica(clientTLS)
	if openErr != nil {
		return fmt.Errorf("read replica module: %w", openErr)
	}
	m.replicaDB = replicaDB

	if err := m.registerResolver(primaryDB); err != nil {
		_ = m.closeReplica()
		return fmt.Errorf("read replica module: register resolver: %w", err)
	}

	mc.Logger.Info("read replica configured", "driver", m.driver())
	return nil
}

func (m *readReplicaModule) Populate(infra *Infrastructure) {
	infra.DBReader = m.replicaDB
}

func (m *readReplicaModule) Close(_ context.Context) error {
	return m.closeReplica()
}

func (m *readReplicaModule) driver() string {
	if m.mysqlCfg != nil {
		return "mysql"
	}
	return "postgres"
}

func (m *readReplicaModule) openReplica(clientTLS *tls.Config) (*gorm.DB, error) {
	if m.mysqlCfg != nil {
		return m.openMySQL(clientTLS)
	}
	return m.openPostgres(clientTLS)
}

func (m *readReplicaModule) openMySQL(clientTLS *tls.Config) (*gorm.DB, error) {
	var opts []gormmysql.Option
	if clientTLS != nil {
		opts = append(opts, gormmysql.WithTLS(clientTLS))
	}
	m.logger.Info("connecting to read replica", "driver", "mysql",
		"host", m.mysqlCfg.Host, "database", m.mysqlCfg.Name)
	return gormmysql.New(*m.mysqlCfg, m.poolCfg, m.logger, opts...)
}

func (m *readReplicaModule) openPostgres(clientTLS *tls.Config) (*gorm.DB, error) {
	var opts []gormpostgres.Option
	if clientTLS != nil {
		opts = append(opts, gormpostgres.WithTLS(clientTLS))
	}
	m.logger.Info("connecting to read replica", "driver", "postgres",
		"host", m.pgCfg.Host, "database", m.pgCfg.Name)
	return gormpostgres.New(*m.pgCfg, m.poolCfg, m.logger, opts...)
}

func (m *readReplicaModule) registerResolver(primaryDB *gorm.DB) error {
	return primaryDB.Use(dbresolver.Register(dbresolver.Config{
		Replicas: []gorm.Dialector{m.replicaDB.Dialector},
		Policy:   dbresolver.RandomPolicy{},
	}))
}

func (m *readReplicaModule) closeReplica() error {
	if m.replicaDB == nil {
		return nil
	}
	sqlDB, err := m.replicaDB.DB()
	if err != nil {
		m.logger.Warn("cannot get underlying sql.DB for replica close", "error", err)
		return err
	}
	if closeErr := sqlDB.Close(); closeErr != nil {
		m.logger.Warn("error closing read replica", "error", closeErr)
		return closeErr
	}
	m.replicaDB = nil
	return nil
}
