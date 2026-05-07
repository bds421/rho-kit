package migrate

import (
	"context"
	"database/sql"
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
	sqlDB, err := extractSQL(db)
	if err != nil {
		return 0, err
	}
	return UpDB(ctx, sqlDB, cfg)
}

// Down rolls back the last migration.
func Down(ctx context.Context, db *gorm.DB, cfg Config) error {
	sqlDB, err := extractSQL(db)
	if err != nil {
		return err
	}
	return DownDB(ctx, sqlDB, cfg)
}

// Status logs the current migration status to the provided logger.
func Status(ctx context.Context, db *gorm.DB, cfg Config, logger *slog.Logger) error {
	sqlDB, err := extractSQL(db)
	if err != nil {
		return err
	}
	return StatusDB(ctx, sqlDB, cfg, logger)
}

// UpDB applies all pending migrations against an explicit *sql.DB.
// Use this when the caller manages a non-GORM driver (for example pgx-native).
func UpDB(ctx context.Context, db *sql.DB, cfg Config) (int, error) {
	provider, err := newProviderFromSQL(db, cfg)
	if err != nil {
		return 0, err
	}

	results, err := provider.Up(ctx)
	if err != nil {
		return len(results), fmt.Errorf("migrate: up: %w", err)
	}
	return len(results), nil
}

// DownDB rolls back the last migration against an explicit *sql.DB.
func DownDB(ctx context.Context, db *sql.DB, cfg Config) error {
	provider, err := newProviderFromSQL(db, cfg)
	if err != nil {
		return err
	}

	if _, err := provider.Down(ctx); err != nil {
		return fmt.Errorf("migrate: down: %w", err)
	}
	return nil
}

// StatusDB logs the current migration status against an explicit *sql.DB.
func StatusDB(ctx context.Context, db *sql.DB, cfg Config, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}

	provider, err := newProviderFromSQL(db, cfg)
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

// extractSQL pulls the underlying *sql.DB out of GORM. Used by the
// gorm-based entry points to delegate to the *sql.DB-based ones.
func extractSQL(db *gorm.DB) (*sql.DB, error) {
	if db == nil {
		return nil, fmt.Errorf("migrate: gorm.DB must not be nil")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("migrate: extract sql.DB from gorm: %w", err)
	}
	return sqlDB, nil
}

// newProviderFromSQL creates a goose provider from a *sql.DB and config.
func newProviderFromSQL(db *sql.DB, cfg Config) (*goose.Provider, error) {
	if db == nil {
		return nil, fmt.Errorf("migrate: db must not be nil")
	}
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

	provider, err := goose.NewProvider(dialect, db, cfg.Dir)
	if err != nil {
		return nil, fmt.Errorf("migrate: create provider: %w", err)
	}
	return provider, nil
}
