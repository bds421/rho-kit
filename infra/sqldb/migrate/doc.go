// Package migrate provides database migration support backed by [goose/v3].
//
// Migrations are loaded from an [fs.FS] (typically an [embed.FS]) and applied
// against a [*gorm.DB]. The package extracts the underlying [*sql.DB] from
// GORM automatically.
//
// # GORM Models + Goose Migrations Workflow
//
// The recommended approach is to pass both GORM models and goose migrations to
// the Builder. The kit automatically selects the right strategy based on the
// ENVIRONMENT variable:
//
//   - development: GORM AutoMigrate runs (fast iteration, no SQL files needed)
//   - production/staging: goose SQL migrations run (controlled, reviewable, reversible)
//
// Example:
//
//	//go:embed migrations/*.sql
//	var migrationsFS embed.FS
//
//	app.New("my-svc", "v1.0.0", cfg).
//	    WithPostgres(dbCfg, poolCfg, &User{}, &Order{}).
//	    WithMigrations(migrationsFS).
//	    Router(routerFn).
//	    Run()
//
// This single configuration works in all environments — no if/else needed.
//
// If only models are passed (no WithMigrations), AutoMigrate always runs.
// If only WithMigrations is passed (no models), goose always runs.
//
// # Writing Migration Files
//
// When you change a GORM model, write the corresponding SQL migration:
//
//  1. Modify your GORM struct (add field, change type, add index, etc.)
//  2. Create a new numbered SQL file in your migrations/ directory
//  3. Write the Up (apply) and Down (rollback) SQL by hand
//
// Each file uses goose annotations and follows a numbered naming convention:
//
//	migrations/
//	  00001_create_users.sql
//	  00002_add_email_index.sql
//	  00003_add_orders_table.sql
//
// Example — adding a field to a GORM model:
//
//	// Model change: added Email field
//	type User struct {
//	    ID    uint   `gorm:"primaryKey"`
//	    Name  string `gorm:"not null"`
//	    Email string `gorm:"uniqueIndex;not null"`  // new field
//	}
//
// Corresponding migration file (00002_add_user_email.sql):
//
//	-- +goose Up
//	ALTER TABLE users ADD COLUMN email VARCHAR(255) NOT NULL DEFAULT '';
//	CREATE UNIQUE INDEX idx_users_email ON users (email);
//
//	-- +goose Down
//	DROP INDEX idx_users_email;
//	ALTER TABLE users DROP COLUMN email;
//
// # GORM Tag → SQL Reference
//
// Common GORM struct tags and their SQL equivalents:
//
//	gorm:"primaryKey"            → PRIMARY KEY
//	gorm:"not null"              → NOT NULL
//	gorm:"uniqueIndex"           → CREATE UNIQUE INDEX ...
//	gorm:"index"                 → CREATE INDEX ...
//	gorm:"type:text"             → column type TEXT
//	gorm:"default:'active'"      → DEFAULT 'active'
//	gorm:"size:255"              → VARCHAR(255)
//	gorm:"column:custom_name"    → column name override
//	gorm:"foreignKey:UserID"     → FOREIGN KEY (user_id) REFERENCES ...
//
// # Migration File Format
//
// SQL migration files follow the goose naming convention. Each file contains
// up and down sections:
//
//	-- +goose Up
//	CREATE TABLE users (
//	    id BIGSERIAL PRIMARY KEY,
//	    name TEXT NOT NULL
//	);
//
//	-- +goose Down
//	DROP TABLE users;
//
// For multi-statement migrations, use goose statement separators:
//
//	-- +goose Up
//	-- +goose StatementBegin
//	CREATE OR REPLACE FUNCTION update_modified()
//	RETURNS TRIGGER AS $$
//	BEGIN
//	    NEW.updated_at = NOW();
//	    RETURN NEW;
//	END;
//	$$ LANGUAGE plpgsql;
//	-- +goose StatementEnd
//
//	-- +goose Down
//	DROP FUNCTION IF EXISTS update_modified;
//
// # Standalone Usage
//
//	applied, err := migrate.Up(ctx, db, migrate.Config{
//	    Dir:     migrations,
//	    Dialect: "postgres",
//	})
//
//	// Rollback the last migration:
//	err = migrate.Down(ctx, db, migrate.Config{
//	    Dir:     migrations,
//	    Dialect: "postgres",
//	})
//
//	// Print migration status:
//	migrate.Status(ctx, db, cfg, logger)
package migrate
