// Package migrate runs goose-managed PostgreSQL migrations against a
// *sql.DB. v2 dropped MySQL/MariaDB and GORM support — kit consumers
// hand goose a *sql.DB sourced from pgx (or the stdlib pq driver) and
// the migration set is parsed via embed.FS.
//
// Typical wiring:
//
//	//go:embed migrations/*.sql
//	var migrations embed.FS
//
//	if _, err := migrate.Up(ctx, sqlDB, migrate.Config{Dir: migrations}); err != nil { ... }
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/pressly/goose/v3"
)

// Config configures database migrations.
type Config struct {
	// Dir is the filesystem containing migration files. Typically an embed.FS.
	Dir fs.FS
}

// Up applies all pending migrations against the given *sql.DB. Returns
// the number of migrations applied.
func Up(ctx context.Context, db *sql.DB, cfg Config) (int, error) {
	provider, err := newProvider(db, cfg)
	if err != nil {
		return 0, err
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return len(results), fmt.Errorf("migrate: up: %w", err)
	}
	return len(results), nil
}

// Down rolls back the last migration.
func Down(ctx context.Context, db *sql.DB, cfg Config) error {
	provider, err := newProvider(db, cfg)
	if err != nil {
		return err
	}
	if _, err := provider.Down(ctx); err != nil {
		return fmt.Errorf("migrate: down: %w", err)
	}
	return nil
}

// Status logs the current migration status to the provided logger.
func Status(ctx context.Context, db *sql.DB, cfg Config, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	provider, err := newProvider(db, cfg)
	if err != nil {
		return err
	}
	status, err := provider.Status(ctx)
	if err != nil {
		return fmt.Errorf("migrate: status: %w", err)
	}
	for _, s := range status {
		state := "pending"
		if s.State == goose.StateApplied {
			state = "applied"
		}
		logger.Info("migration",
			"version", s.Source.Version,
			redact.String("path", s.Source.Path),
			"state", state,
		)
	}
	return nil
}

// newProvider builds a goose provider for a Postgres *sql.DB.
func newProvider(db *sql.DB, cfg Config) (*goose.Provider, error) {
	if db == nil {
		return nil, fmt.Errorf("migrate: db must not be nil")
	}
	if cfg.Dir == nil {
		return nil, fmt.Errorf("migrate: Dir must not be nil")
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: create provider: %w", err)
	}
	return provider, nil
}
