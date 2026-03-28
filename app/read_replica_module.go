package app

import (
	"context"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
	"github.com/bds421/rho-kit/observability/health"
)

// readReplicaModule opens a read replica connection and exposes it as
// infra.DBReader. It depends on the "database" module being registered first.
type readReplicaModule struct {
	BaseModule

	driver  gormdb.Driver
	cfg     sqldb.Config
	poolCfg sqldb.PoolConfig

	// initialized during Init
	replicaDB *gorm.DB
	logger    *slog.Logger
}

// NewReadReplicaModule creates a Module that connects to a read replica
// database. The replica is available via infra.DBReader in the RouterFunc.
//
// The "database" module must be registered before this module so the primary
// connection is available during Init. Registration order is enforced by the
// module dependency lookup in Init.
func NewReadReplicaModule(driver gormdb.Driver, cfg sqldb.Config, pool sqldb.PoolConfig) Module {
	if driver == nil {
		panic("app: read replica module requires a Driver")
	}
	return &readReplicaModule{
		BaseModule: NewBaseModule("read-replica"),
		driver:     driver,
		cfg:        cfg,
		poolCfg:    pool,
	}
}

func (m *readReplicaModule) Init(_ context.Context, mc ModuleContext) error {
	m.logger = mc.Logger

	// Verify the primary database module is initialized.
	dbMod := mc.Module("database")
	primary, ok := dbMod.(*databaseModule)
	if !ok || primary.DB() == nil {
		return fmt.Errorf("read replica module: primary database not initialized")
	}

	clientTLS, err := mc.Config.TLS.ClientTLS()
	if err != nil {
		return fmt.Errorf("read replica module: build client TLS: %w", err)
	}

	m.logger.Info("connecting to read replica",
		"driver", m.driver.Name(),
		"host", m.cfg.Host,
		"database", m.cfg.Name)

	replicaDB, openErr := m.driver.Open(m.cfg, m.poolCfg, m.logger, clientTLS)
	if openErr != nil {
		return fmt.Errorf("read replica module: %w", openErr)
	}
	m.replicaDB = replicaDB

	mc.Logger.Info("read replica connection configured", "driver", m.driver.Name())
	return nil
}

func (m *readReplicaModule) Populate(infra *Infrastructure) {
	infra.DBReader = m.replicaDB
}

func (m *readReplicaModule) HealthChecks() []health.DependencyCheck {
	if m.replicaDB == nil {
		return nil
	}
	return []health.DependencyCheck{
		{
			Name:     "read-replica",
			Critical: false,
			Check: func(_ context.Context) string {
				pinger := gormdb.NewPinger(m.replicaDB)
				if err := pinger.Ping(); err != nil {
					return health.StatusUnhealthy
				}
				return health.StatusHealthy
			},
		},
	}
}

func (m *readReplicaModule) Close(_ context.Context) error {
	return gormdb.CloseDB(m.replicaDB)
}
