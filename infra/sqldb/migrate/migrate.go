package migrate

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/pressly/goose/v3"
	"gorm.io/gorm"
)

// Config configures database migrations.
type Config struct {
	// Dir is the filesystem containing migration files. Typically an embed.FS.
	Dir fs.FS

	// Dialect is the database type: "postgres" or "mysql".
	Dialect string
}

// Up applies all pending migrations. Returns the number of migrations applied.
func Up(ctx context.Context, db *gorm.DB, cfg Config) (int, error) {
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
func Down(ctx context.Context, db *gorm.DB, cfg Config) error {
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
func Status(ctx context.Context, db *gorm.DB, cfg Config, logger *slog.Logger) error {
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
			"path", s.Source.Path,
			"state", state,
		)
	}
	return nil
}

// mapDialect converts a string dialect to a goose.Dialect.
func mapDialect(dialect string) (goose.Dialect, error) {
	switch dialect {
	case "postgres", "postgresql":
		return goose.DialectPostgres, nil
	case "mysql", "mariadb":
		return goose.DialectMySQL, nil
	case "sqlite", "sqlite3":
		return goose.DialectSQLite3, nil
	default:
		return goose.DialectPostgres, fmt.Errorf("migrate: unsupported dialect %q (use postgres, mysql, or sqlite)", dialect)
	}
}

// newProvider creates a goose provider from the GORM database and config.
func newProvider(db *gorm.DB, cfg Config) (*goose.Provider, error) {
	if cfg.Dir == nil {
		return nil, fmt.Errorf("migrate: Dir must not be nil")
	}
	if cfg.Dialect == "" {
		return nil, fmt.Errorf("migrate: Dialect must not be empty")
	}

	dialect, err := mapDialect(cfg.Dialect)
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("migrate: extract sql.DB from gorm: %w", err)
	}

	provider, err := goose.NewProvider(dialect, sqlDB, cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: create provider: %w", err)
	}
	return provider, nil
}
