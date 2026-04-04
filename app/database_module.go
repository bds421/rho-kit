package app

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
	"github.com/bds421/rho-kit/infra/sqldb/migrate"
	"github.com/bds421/rho-kit/observability/health"
)

// databaseModule implements the Module interface for database connections
// using the unified Driver abstraction. It handles connection setup, schema
// migrations, optional seeding, health checks, pool metrics, and cleanup.
//
// # Credential Rotation
//
// With [Builder.WithSecretRotation], database credentials are rotated live.
// The PostgreSQL driver uses a [gormdb.SwappablePool] that sits between GORM
// and the underlying *sql.DB. On credential change:
//  1. A new *sql.DB is created with the new password
//  2. The SwappablePool atomically swaps to the new pool
//  3. All new queries immediately use the new pool
//  4. The old pool is closed after a grace period (ConnMaxLifetime)
//
// No code changes are needed in repositories — the *gorm.DB pointer stays
// the same. Only the underlying connection pool changes.
type databaseModule struct {
	// config -- set at construction time, never mutated after.
	driver    gormdb.Driver
	cfg       sqldb.Config
	poolCfg   sqldb.PoolConfig
	namespace string

	// optional features
	migrationsDir fs.FS
	seedFn        SeedFunc
	metrics       bool

	// initialized during Init
	db       *gorm.DB
	logger   *slog.Logger
	seedExit bool
}

// databaseModuleConfig holds the configuration for constructing a databaseModule.
type databaseModuleConfig struct {
	driver        gormdb.Driver
	cfg           sqldb.Config
	poolCfg       sqldb.PoolConfig
	namespace     string
	migrationsDir fs.FS
	seedFn        SeedFunc
	metrics       bool
}

// newDatabaseModule creates a database module from the given config.
// Panics if no driver is set.
func newDatabaseModule(dmCfg databaseModuleConfig) *databaseModule {
	if dmCfg.driver == nil {
		panic("app: database module requires a Driver")
	}
	return &databaseModule{
		driver:        dmCfg.driver,
		cfg:           dmCfg.cfg,
		poolCfg:       dmCfg.poolCfg,
		namespace:     dmCfg.namespace,
		migrationsDir: dmCfg.migrationsDir,
		seedFn:        dmCfg.seedFn,
		metrics:       dmCfg.metrics,
	}
}

func (m *databaseModule) Name() string { return "database" }

func (m *databaseModule) Init(_ context.Context, mc ModuleContext) error {
	m.logger = mc.Logger

	clientTLS, err := mc.Config.TLS.ClientTLS()
	if err != nil {
		return fmt.Errorf("database module: build client TLS: %w", err)
	}

	m.logger.Info("connecting to database",
		"driver", m.driver.Name(),
		"host", m.cfg.Host,
		"database", m.cfg.Name)

	db, openErr := m.driver.Open(m.cfg, m.poolCfg, m.logger, clientTLS)
	if openErr != nil {
		return fmt.Errorf("database module: %w", openErr)
	}
	m.db = db

	if err := m.runMigrations(); err != nil {
		_ = m.closeDB()
		return err
	}

	if err := m.runSeed(); err != nil {
		_ = m.closeDB()
		return err
	}

	if m.metrics {
		m.registerMetrics(mc)
	}

	mc.Logger.Info("database connection configured", "driver", m.driver.Name())
	return nil
}

func (m *databaseModule) HealthChecks() []health.DependencyCheck {
	if m.db == nil {
		return nil
	}
	return []health.DependencyCheck{sqldb.HealthCheck(gormdb.NewPinger(m.db))}
}

func (m *databaseModule) Populate(infra *Infrastructure) {
	infra.DB = m.db
	// When no read replica is configured, readers fall back to the primary.
	if infra.DBReader == nil {
		infra.DBReader = m.db
	}
}

func (m *databaseModule) Close(_ context.Context) error {
	return m.closeDB()
}

// closeDB closes the underlying sql.DB connection pool and nils m.db.
// Called from Close (normal shutdown) and from Init when a post-open step
// (migrations, seeding) fails. Without this, a failed Init would leak the
// connection because initModules only calls Close on already-initialized
// modules, not the one whose Init returned an error.
func (m *databaseModule) closeDB() error {
	if m.db == nil {
		return nil
	}
	sqlDB, err := m.db.DB()
	if err != nil {
		m.logger.Warn("cannot get underlying sql.DB for close", "error", err)
		return err
	}
	if closeErr := sqlDB.Close(); closeErr != nil {
		m.logger.Warn("error closing database", "error", closeErr)
		return closeErr
	}
	m.db = nil
	return nil
}

// DB returns the initialized gorm.DB, or nil if Init has not been called.
func (m *databaseModule) DB() *gorm.DB {
	return m.db
}

// SeedExit reports whether the --seed flag was processed and the service
// should exit cleanly after module initialization.
func (m *databaseModule) SeedExit() bool {
	return m.seedExit
}

func (m *databaseModule) runMigrations() error {
	if m.migrationsDir == nil {
		return nil
	}
	applied, err := migrate.Up(context.Background(), m.db, migrate.Config{
		Dir:     m.migrationsDir,
		Dialect: m.driver.Name(),
	})
	if err != nil {
		return fmt.Errorf("database migrations failed: %w", err)
	}
	if applied > 0 {
		m.logger.Info("database migrations applied", "count", applied)
	}
	return nil
}

func (m *databaseModule) runSeed() error {
	if m.seedFn == nil {
		return nil
	}
	seedPath := parseSeedFlag()
	if seedPath == "" {
		return nil
	}
	if err := m.seedFn(m.db, seedPath, m.logger); err != nil {
		return err
	}
	m.logger.Info("seed completed, exiting")
	m.seedExit = true
	return nil
}

func (m *databaseModule) registerMetrics(mc ModuleContext) {
	dbMetrics := sqldb.NewPoolMetrics(m.namespace, nil)
	mc.Runner.AddFunc("db-pool-metrics", func(ctx context.Context) error {
		sqlDB, dbErr := m.db.DB()
		if dbErr != nil {
			mc.Logger.Warn("cannot export DB pool metrics", "error", dbErr)
			return nil
		}
		sqldb.ExportPoolMetrics(ctx, sqlDB, dbMetrics, 15*time.Second)
		return nil
	})
}
