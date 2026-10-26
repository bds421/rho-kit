// Package migrate runs goose-managed PostgreSQL migrations against a
// *sql.DB. Consumers hand goose a *sql.DB sourced from pgx (or the
// stdlib pq driver) and the migration set is parsed via embed.FS.
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
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"strings"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/pressly/goose/v3"
)

// DefaultTableName is the goose version table used when [Config.TableName]
// is empty.
const DefaultTableName = goose.DefaultTablename

// maxIdentifierLen is the PostgreSQL identifier length limit (NAMEDATALEN-1).
const maxIdentifierLen = 63

// Config configures database migrations.
type Config struct {
	// Dir is the filesystem containing migration files. Typically an embed.FS.
	Dir fs.FS

	// TableName is the goose version table that records which migrations of
	// this set have been applied. Empty means [DefaultTableName]
	// ("goose_db_version"), which keeps existing databases unchanged.
	//
	// Give each independently owned migration set its own table so several
	// sets can share one database without their version numbers colliding.
	// The name must be an unquoted PostgreSQL identifier, optionally
	// schema-qualified ("schema.table"): letters, digits and underscores,
	// not starting with a digit, at most 63 bytes per part.
	TableName string
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
		return len(results), redact.WrapError("migrate: up", err)
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
		return redact.WrapError("migrate: down", err)
	}
	return nil
}

// DownTo rolls back every applied migration newer than version, leaving
// the schema as it was right after version (0 rolls back everything).
// Unlike repeated Down calls, migrations added later cannot change where
// it ends.
func DownTo(ctx context.Context, db *sql.DB, cfg Config, version int64) error {
	if version < 0 {
		return errors.New("migrate: down to a negative version")
	}
	provider, err := newProvider(db, cfg)
	if err != nil {
		return err
	}
	if _, err := provider.DownTo(ctx, version); err != nil {
		return redact.WrapError("migrate: down to", err)
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
		return redact.WrapError("migrate: status", err)
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
	var opts []goose.ProviderOption
	if cfg.TableName != "" {
		if err := validateTableName(cfg.TableName); err != nil {
			return nil, err
		}
		opts = append(opts, goose.WithTableName(cfg.TableName))
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, cfg.Dir, opts...)
	if err != nil {
		return nil, redact.WrapError("migrate: create provider", err)
	}
	return provider, nil
}

// validateTableName rejects anything other than an unquoted, optionally
// schema-qualified identifier. goose interpolates the table name into its
// SQL verbatim, so the check is also the injection guard.
func validateTableName(name string) error {
	parts := strings.Split(name, ".")
	if len(parts) > 2 {
		return fmt.Errorf("migrate: TableName %q has more than one schema qualifier", name)
	}
	for _, part := range parts {
		if !isIdentifier(part) {
			return fmt.Errorf("migrate: TableName %q is not a valid unquoted identifier", name)
		}
	}
	return nil
}

func isIdentifier(s string) bool {
	if s == "" || len(s) > maxIdentifierLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}
