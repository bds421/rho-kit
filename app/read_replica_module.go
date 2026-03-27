package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormmysql"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormpostgres"
	"github.com/bds421/rho-kit/observability/health"
)

// readReplicaModule implements the Module interface for database read replicas.
// It opens a replica connection, delegates DBResolver registration to
// [gormdb.RegisterReplica], and exposes the replica as infra.DBReader.
//
// This module depends on the "database" module being initialized first.
type readReplicaModule struct {
	BaseModule

	// config -- set at construction time, never mutated after.
	cfg gormdb.ReplicaConfig

	// initialized during Init
	replicaDB *gorm.DB
	logger    *slog.Logger
}

// NewReadReplicaModule creates a read replica module for use with
// [Builder.WithModule]. The ReplicaConfig must have exactly one of
// MySQL or Postgres set; validation happens at Init time.
//
// Usage:
//
//	builder.WithModule(app.NewReadReplicaModule(gormdb.ReplicaConfig{
//	    Postgres: &sqldb.PostgresConfig{Host: "replica.db"},
//	    Pool:     sqldb.PoolConfig{MaxOpenConns: 10},
//	}))
func NewReadReplicaModule(cfg gormdb.ReplicaConfig) Module {
	return &readReplicaModule{
		BaseModule: NewBaseModule("read-replica"),
		cfg:        cfg,
	}
}

func (m *readReplicaModule) Init(_ context.Context, mc ModuleContext) error {
	m.logger = mc.Logger

	if err := m.cfg.Validate(); err != nil {
		return fmt.Errorf("read replica module: %w", err)
	}

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

	if regErr := gormdb.RegisterReplica(primaryDB, replicaDB, mc.Logger); regErr != nil {
		_ = gormdb.CloseDB(replicaDB)
		m.replicaDB = nil
		return fmt.Errorf("read replica module: %w", regErr)
	}

	mc.Logger.Info("read replica configured", "driver", m.cfg.Driver())
	return nil
}

func (m *readReplicaModule) Populate(infra *Infrastructure) {
	infra.DBReader = m.replicaDB
}

func (m *readReplicaModule) HealthChecks() []health.DependencyCheck {
	if m.replicaDB == nil {
		return nil
	}
	pinger := gormdb.NewPinger(m.replicaDB)
	return []health.DependencyCheck{{
		Name: "database-replica",
		Check: func(_ context.Context) string {
			if err := pinger.Ping(); err != nil {
				return health.StatusDegraded
			}
			return health.StatusHealthy
		},
		Critical: false,
	}}
}

func (m *readReplicaModule) Close(_ context.Context) error {
	if err := gormdb.CloseDB(m.replicaDB); err != nil {
		m.logger.Warn("error closing read replica", "error", err)
		return err
	}
	m.replicaDB = nil
	return nil
}

func (m *readReplicaModule) openReplica(clientTLS *tls.Config) (*gorm.DB, error) {
	if m.cfg.MySQL != nil {
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
		"host", m.cfg.MySQL.Host, "database", m.cfg.MySQL.Name)
	return gormmysql.New(*m.cfg.MySQL, m.cfg.Pool, m.logger, opts...)
}

func (m *readReplicaModule) openPostgres(clientTLS *tls.Config) (*gorm.DB, error) {
	var opts []gormpostgres.Option
	if clientTLS != nil {
		opts = append(opts, gormpostgres.WithTLS(clientTLS))
	}
	m.logger.Info("connecting to read replica", "driver", "postgres",
		"host", m.cfg.Postgres.Host, "database", m.cfg.Postgres.Name)
	return gormpostgres.New(*m.cfg.Postgres, m.cfg.Pool, m.logger, opts...)
}
