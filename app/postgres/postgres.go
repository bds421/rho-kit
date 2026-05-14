package postgres

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/bds421/rho-kit/app/v2"
	pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
	"github.com/bds421/rho-kit/infra/v2/sqldb/migrate"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// ResourceKey is the Infrastructure.Resource key under which the Module
// publishes its initialized [*pgxbackend.Pool]. Use [Pool] to retrieve the
// typed handle.
const ResourceKey = "github.com/bds421/rho-kit/app/postgres"

// Option configures the Postgres [Module] before Builder.Run executes it.
type Option func(*moduleConfig)

type moduleConfig struct {
	cfg           pgxbackend.Config
	migrationsDir fs.FS
	registerer    prometheus.Registerer
	instance      string
}

// WithMigrations attaches a goose-managed migration tree. Migrations run
// inside [Module.Init] before the pool is published; a failure aborts startup
// so the service never serves traffic against a stale schema.
func WithMigrations(dir fs.FS) Option {
	if dir == nil {
		panic("postgres: WithMigrations requires a non-nil fs.FS")
	}
	return func(c *moduleConfig) {
		c.migrationsDir = dir
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
	return &pgxModule{cfg: mc}
}

// pgxModule wires a pgx-native Postgres pool into the Builder.
type pgxModule struct {
	app.BaseModule

	cfg  moduleConfig
	pool *pgxbackend.Pool
	log  *slog.Logger
}

func (m *pgxModule) Name() string { return "postgres" }

func (m *pgxModule) Init(ctx context.Context, mc app.ModuleContext) error {
	m.log = mc.Logger
	pool, err := pgxbackend.Connect(ctx, m.cfg.cfg)
	if err != nil {
		return fmt.Errorf("postgres module: %w", err)
	}
	m.pool = pool
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
			slog.Any("error", err),
		)
	}

	if m.cfg.migrationsDir != nil {
		if err := m.runMigrations(ctx); err != nil {
			_ = pool.Close()
			m.pool = nil
			return err
		}
	}
	return nil
}

func (m *pgxModule) runMigrations(ctx context.Context) error {
	sqlDB := stdlib.OpenDBFromPool(m.pool.Pool())
	defer func() { _ = sqlDB.Close() }()

	applied, err := migrate.Up(ctx, sqlDB, migrate.Config{Dir: m.cfg.migrationsDir})
	if err != nil {
		return fmt.Errorf("postgres module: migrations failed: %w", err)
	}
	if applied > 0 {
		m.log.Info("database migrations applied", "count", applied)
	}
	return nil
}

func (m *pgxModule) HealthChecks() []health.DependencyCheck {
	if m.pool == nil {
		return nil
	}
	return []health.DependencyCheck{
		{
			Name: "postgres",
			Check: func(ctx context.Context) string {
				if err := m.pool.Ping(ctx); err != nil {
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

func (m *pgxModule) Stop(_ context.Context) error {
	if m == nil || m.pool == nil {
		return nil
	}
	pool := m.pool
	m.pool = nil
	return pool.Close()
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
