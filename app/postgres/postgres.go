package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/app/v2"
	"github.com/bds421/rho-kit/core/v2/redact"
	pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
	"github.com/bds421/rho-kit/infra/v2/sqldb/migrate"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// SQLDBProvider exposes a stdlib [*sql.DB] backed by the module's pgx
// pool. [github.com/bds421/rho-kit/app/leader/v2].PGAdvisoryFromPostgres
// looks this up at Init time; register postgres before leader.
type SQLDBProvider interface {
	SQLDB() *sql.DB
}

// SQLDBReadyProvider is an optional capability on a postgres module that
// reports whether [SQLDBProvider.SQLDB] is safe to call. Peer modules
// registered before postgres use this to surface an actionable ordering
// error instead of a panic from SQLDB.
type SQLDBReadyProvider interface {
	SQLDBReady() bool
}

// ResourceKey is the Infrastructure.Resource key under which the Module
// publishes its initialized [*pgxbackend.Pool]. Use [Pool] to retrieve the
// typed handle.
const ResourceKey = "github.com/bds421/rho-kit/app/postgres"

// Option configures the Postgres [Module] before Builder.Run executes it.
type Option func(*moduleConfig)

type moduleConfig struct {
	cfg        pgxbackend.Config
	migrations []migrationSet
	registerer prometheus.Registerer
	instance   string
}

// migrationSet is one goose migration tree tracked in its own version table.
// An empty table means the migrate package default ("goose_db_version").
type migrationSet struct {
	table string
	dir   fs.FS
}

// WithMigrations attaches a goose-managed migration tree tracked in the
// default goose version table ("goose_db_version"). Migrations run inside
// [Module.Init] before the pool is published; a failure aborts startup so the
// service never serves traffic against a stale schema. Calling it again
// replaces the previous tree.
func WithMigrations(dir fs.FS) Option {
	if dir == nil {
		panic("postgres: WithMigrations requires a non-nil fs.FS")
	}
	return func(c *moduleConfig) {
		c.setMigrations(migrationSet{dir: dir})
	}
}

// WithMigrationSet attaches an additional goose-managed migration tree whose
// progress is recorded in its own version table, so independently owned
// modules can keep separate migration histories in one database. Sets run
// inside [Module.Init] in registration order (together with the
// [WithMigrations] tree, in option order); the first failure aborts startup.
//
// table must be an unquoted, optionally schema-qualified PostgreSQL
// identifier (see [migrate.Config.TableName]); it is validated when the
// migrations run. Panics on an empty table, a nil dir, or, at [Module], a
// table registered twice (including "goose_db_version" alongside
// [WithMigrations]).
func WithMigrationSet(table string, dir fs.FS) Option {
	if table == "" {
		panic("postgres: WithMigrationSet requires a non-empty table name")
	}
	if dir == nil {
		panic("postgres: WithMigrationSet requires a non-nil fs.FS")
	}
	return func(c *moduleConfig) {
		c.migrations = append(c.migrations, migrationSet{table: table, dir: dir})
	}
}

// setMigrations replaces the default-table set in place, or appends it.
func (c *moduleConfig) setMigrations(set migrationSet) {
	for i := range c.migrations {
		if c.migrations[i].table == "" {
			c.migrations[i] = set
			return
		}
	}
	c.migrations = append(c.migrations, set)
}

// validateMigrationSets panics when two sets share a version table.
func (c *moduleConfig) validateMigrationSets() {
	seen := make(map[string]bool, len(c.migrations))
	for _, set := range c.migrations {
		table := set.table
		if table == "" {
			table = migrate.DefaultTableName
		}
		if seen[table] {
			panic(fmt.Sprintf("postgres: migration table %q registered more than once", table))
		}
		seen[table] = true
	}
}

// WithRegisterer overrides the Prometheus registerer used for pgxpool stat
// collectors. Default is [prometheus.DefaultRegisterer]. Multi-pool services
// can supply distinct registries to keep metric names disjoint.
func WithRegisterer(r prometheus.Registerer) Option {
	return func(c *moduleConfig) {
		c.registerer = r
	}
}

// WithInstance overrides the pool's `instance` label on Prometheus metrics.
// Default is "primary". Use a unique value when a service runs multiple
// independent pools (e.g., one per shard).
func WithInstance(name string) Option {
	if name == "" {
		panic("postgres: WithInstance requires a non-empty name")
	}
	return func(c *moduleConfig) {
		c.instance = name
	}
}

// Module returns an [app.Module] that opens and supervises a pgx-native
// Postgres pool. Pass to [app.Builder.With].
//
// Panics if cfg.DSN is empty. In non-dev, sslmode must be require/verify-ca/
// verify-full (enforced inside [pgxbackend.Connect]).
func Module(cfg pgxbackend.Config, opts ...Option) app.Module {
	if cfg.DSN == "" {
		panic("postgres: Module requires a non-empty DSN")
	}
	mc := moduleConfig{
		cfg:      cfg,
		instance: "primary",
	}
	for _, opt := range opts {
		if opt == nil {
			panic("postgres: Module option must not be nil")
		}
		opt(&mc)
	}
	mc.validateMigrationSets()
	return &pgxModule{cfg: mc}
}

// pgxModule wires a pgx-native Postgres pool into the Builder.
type pgxModule struct {
	app.BaseModule

	cfg   moduleConfig
	pool  *pgxbackend.Pool
	sqlDB *sql.DB
	log   *slog.Logger
}

// ModuleName is the registered Module.Name() value.
const ModuleName = "postgres"

func (m *pgxModule) Name() string { return ModuleName }

// SQLDB returns the stdlib database handle over the module pool. The
// handle is created once during Init and must not be closed by callers —
// it shares the pool's lifetime.
func (m *pgxModule) SQLDB() *sql.DB {
	if m.sqlDB == nil {
		panic("postgres: SQLDB called before Init completed")
	}
	return m.sqlDB
}

// SQLDBReady reports whether [pgxModule.SQLDB] is safe to call.
func (m *pgxModule) SQLDBReady() bool {
	return m != nil && m.sqlDB != nil
}

func (m *pgxModule) Init(ctx context.Context, mc app.ModuleContext) error {
	m.log = mc.Logger
	pool, err := pgxbackend.Connect(ctx, m.cfg.cfg)
	if err != nil {
		return fmt.Errorf("postgres module: %w", err)
	}
	m.pool = pool
	m.sqlDB = stdlib.OpenDBFromPool(pool.Pool())
	mc.Logger.Info("postgres pool configured")

	// Wire the pgxpool stat collector to Prometheus. The collector reads
	// pool.Stat() at scrape time, so there is no background goroutine to
	// manage. Failure to register is non-fatal: pool capacity dashboards
	// degrade, but the service itself runs.
	var statsOpts []pgxbackend.MetricsOption
	if m.cfg.registerer != nil {
		statsOpts = append(statsOpts, pgxbackend.WithRegisterer(m.cfg.registerer))
	}
	if _, err := pgxbackend.NewPoolStatsCollector(pool.Pool(), m.cfg.instance, statsOpts...); err != nil {
		mc.Logger.Warn("postgres pool stats collector not registered",
			redact.Error(err),
		)
	}

	if len(m.cfg.migrations) > 0 {
		if err := m.runMigrations(ctx); err != nil {
			if m.sqlDB != nil {
				_ = m.sqlDB.Close()
				m.sqlDB = nil
			}
			_ = pool.Close()
			m.pool = nil
			return err
		}
	}
	return nil
}

func (m *pgxModule) runMigrations(ctx context.Context) error {
	for _, set := range m.cfg.migrations {
		applied, err := migrate.Up(ctx, m.sqlDB, migrate.Config{Dir: set.dir, TableName: set.table})
		if err != nil {
			if set.table == "" {
				return fmt.Errorf("postgres module: migrations failed: %w", err)
			}
			return fmt.Errorf("postgres module: migrations for table %q failed: %w", set.table, err)
		}
		if applied > 0 {
			if set.table == "" {
				m.log.Info("database migrations applied", "count", applied)
			} else {
				m.log.Info("database migrations applied", "count", applied, "table", set.table)
			}
		}
	}
	return nil
}

func (m *pgxModule) HealthChecks() []health.DependencyCheck {
	if m.pool == nil {
		return nil
	}
	// Capture the pool in a local so the check closure never re-reads the
	// mutable m.pool field. Stop nils m.pool during shutdown, and a lingering
	// or timed-out check goroutine can outlive Stop; reading m.pool here would
	// race that write. Pool.Close is safe to call while a captured Ping runs.
	pool := m.pool
	return []health.DependencyCheck{
		{
			Name: "postgres",
			Check: func(ctx context.Context) string {
				if err := pool.Ping(ctx); err != nil {
					return health.StatusUnhealthy
				}
				return health.StatusHealthy
			},
		},
	}
}

func (m *pgxModule) Populate(infra *app.Infrastructure) {
	if m.pool == nil {
		return
	}
	infra.SetResource(ResourceKey, m.pool)
}

func (m *pgxModule) Stop(ctx context.Context) error {
	if m == nil || m.pool == nil {
		return nil
	}
	pool := m.pool
	m.pool = nil
	if m.sqlDB != nil {
		// Close the derived *sql.DB first so its connectionOpener goroutine
		// exits; OpenDBFromPool sets MaxIdleConns(0) but Close is still
		// required to release the stdlib bookkeeping.
		_ = m.sqlDB.Close()
		m.sqlDB = nil
	}
	// pool.Close blocks until acquired connections return. Bound it by
	// the shutdown context so a stuck borrower cannot hang process exit.
	done := make(chan struct{})
	go func() {
		_ = pool.Close()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Pool returns the Postgres pool published by the [Module] under
// [ResourceKey], or nil if no postgres adapter was registered with the
// Builder. Use this inside [app.RouterFunc] to access the pool:
//
//	app.New("svc", "v1", base).
//	    With(postgres.Module(cfg)).
//	    Router(func(infra app.Infrastructure) http.Handler {
//	        pool := postgres.Pool(infra)
//	        // pool is *pgxbackend.Pool
//	    })
func Pool(infra app.Infrastructure) *pgxbackend.Pool {
	v, ok := infra.Resource(ResourceKey)
	if !ok {
		return nil
	}
	pool, _ := v.(*pgxbackend.Pool)
	return pool
}
