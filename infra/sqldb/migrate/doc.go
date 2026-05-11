// Package migrate provides PostgreSQL schema migration support backed by
// [goose/v3].
//
// Migrations are loaded from an [fs.FS] (typically an [embed.FS]) and applied
// against a [*sql.DB]. Builder users normally call [app.Builder.WithMigrations],
// which runs migrations through the configured pgx pool during startup.
//
// # Builder Usage
//
//	//go:embed migrations/*.sql
//	var migrationsFS embed.FS
//
//	app.New("my-svc", "v1.0.0", cfg).
//	    WithPostgres(dbCfg).
//	    WithMigrations(migrationsFS).
//	    Router(routerFn).
//	    Run()
//
// # Standalone Usage
//
//	pool, err := pgxbackend.Connect(ctx, dbCfg)
//	if err != nil {
//	    return err
//	}
//	defer pool.Close()
//
//	sqlDB := stdlib.OpenDBFromPool(pool.Pool())
//	defer sqlDB.Close()
//
//	applied, err := migrate.Up(ctx, sqlDB, migrate.Config{Dir: migrationsFS})
//
// # Migration File Format
//
// SQL migration files follow the goose naming convention. Each file contains
// up and down sections:
//
//	-- +goose Up
//	CREATE TABLE users (
//	    id UUID PRIMARY KEY,
//	    email TEXT NOT NULL UNIQUE
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
// Migrations should be reviewed SQL. v2 does not generate schema from GORM
// models.
package migrate
