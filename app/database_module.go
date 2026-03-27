package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"io/fs"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormmysql"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb/gormpostgres"
	"github.com/bds421/rho-kit/infra/sqldb/migrate"
	"github.com/bds421/rho-kit/observability/health"
)

// databaseModule implements the Module interface for MySQL and PostgreSQL
// connections. It handles connection setup, schema migrations, optional
// seeding, health checks, pool metrics, and cleanup.
type databaseModule struct {
	// config -- set at construction time, never mutated after.
	mysqlCfg  *sqldb.MySQLConfig
	pgCfg     *sqldb.PostgresConfig
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
	mysqlCfg      *sqldb.MySQLConfig
	pgCfg         *sqldb.PostgresConfig
	poolCfg       sqldb.PoolConfig
	namespace     string
	migrationsDir fs.FS
	seedFn        SeedFunc
	metrics       bool
}

// newDatabaseModule creates a database module from the given config.
// Panics if neither MySQL nor Postgres config is set.
func newDatabaseModule(cfg databaseModuleConfig) *databaseModule {
	if cfg.mysqlCfg == nil && cfg.pgCfg == nil {
		panic("app: database module requires MySQL or Postgres config")
	}
	return &databaseModule{
		mysqlCfg:      cfg.mysqlCfg,
		pgCfg:         cfg.pgCfg,
		poolCfg:       cfg.poolCfg,
		namespace:     cfg.namespace,
		migrationsDir: cfg.migrationsDir,
		seedFn:        cfg.seedFn,
		metrics:       cfg.metrics,
	}
}

func (m *databaseModule) Name() string { return "database" }

func (m *databaseModule) Init(_ context.Context, mc ModuleContext) error {
	m.logger = mc.Logger

	clientTLS, err := mc.Config.TLS.ClientTLS()
	if err != nil {
		return fmt.Errorf("database module: build client TLS: %w", err)
	}

	db, openErr := m.openDB(clientTLS)
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

	mc.Logger.Info("database connection configured", "driver", m.driver())
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

// driver returns the database driver name for logging.
func (m *databaseModule) driver() string {
	if m.mysqlCfg != nil {
		return "mysql"
	}
	return "postgres"
}

func (m *databaseModule) openDB(clientTLS *tls.Config) (*gorm.DB, error) {
	if m.mysqlCfg != nil {
		return m.openMySQL(clientTLS)
	}
	return m.openPostgres(clientTLS)
}

func (m *databaseModule) openMySQL(clientTLS *tls.Config) (*gorm.DB, error) {
	var dbOpts []gormmysql.Option
	if clientTLS != nil {
		dbOpts = append(dbOpts, gormmysql.WithTLS(clientTLS))
	}
	m.logger.Info("connecting to database", "driver", "mysql",
		"host", m.mysqlCfg.Host, "database", m.mysqlCfg.Name)
	return gormmysql.New(*m.mysqlCfg, m.poolCfg, m.logger, dbOpts...)
}

func (m *databaseModule) openPostgres(clientTLS *tls.Config) (*gorm.DB, error) {
	var dbOpts []gormpostgres.Option
	if clientTLS != nil {
		dbOpts = append(dbOpts, gormpostgres.WithTLS(clientTLS))
	}
	m.logger.Info("connecting to database", "driver", "postgres",
		"host", m.pgCfg.Host, "database", m.pgCfg.Name)
	return gormpostgres.New(*m.pgCfg, m.poolCfg, m.logger, dbOpts...)
}

func (m *databaseModule) runMigrations() error {
	if m.migrationsDir == nil {
		return nil
	}
	applied, err := migrate.Up(context.Background(), m.db, migrate.Config{
		Dir:     m.migrationsDir,
		Dialect: m.driver(),
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
