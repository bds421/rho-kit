package app

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5/stdlib"

	pgxbackend "github.com/bds421/rho-kit/infra/sqldb/pgx/v2"
	"github.com/bds421/rho-kit/infra/v2/sqldb/migrate"
	"github.com/bds421/rho-kit/observability/v2/health"
)

// pgxModule wires a pgx-native Postgres pool into the Builder.
type pgxModule struct {
	BaseModule

	cfg           pgxbackend.Config
	migrationsDir fs.FS
	pool          *pgxbackend.Pool
	log           *slog.Logger
}

func newPgxModule(cfg pgxbackend.Config, migrationsDir fs.FS) *pgxModule {
	if cfg.DSN == "" {
		panic("app: WithPostgres requires a non-empty DSN")
	}
	return &pgxModule{
		BaseModule:    NewBaseModule("pgx"),
		cfg:           cfg,
		migrationsDir: migrationsDir,
	}
}

func (m *pgxModule) Init(ctx context.Context, mc ModuleContext) error {
	m.log = mc.Logger
	pool, err := pgxbackend.Connect(ctx, m.cfg)
	if err != nil {
		return fmt.Errorf("pgx module: %w", err)
	}
	m.pool = pool
	mc.Logger.Info("pgx pool configured")

	if m.migrationsDir != nil {
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

	applied, err := migrate.Up(ctx, sqlDB, migrate.Config{Dir: m.migrationsDir})
	if err != nil {
		return fmt.Errorf("pgx module: migrations failed: %w", err)
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
			Name: "pgx",
			Check: func(ctx context.Context) string {
				if err := m.pool.Ping(ctx); err != nil {
					return health.StatusUnhealthy
				}
				return health.StatusHealthy
			},
		},
	}
}

func (m *pgxModule) Populate(infra *Infrastructure) {
	infra.DB = m.pool
}

func (m *pgxModule) Close(_ context.Context) error {
	if m.pool == nil {
		return nil
	}
	return m.pool.Close()
}
