package postgres

import "embed"

// Migrations contains the SQL migrations for the audit_log_events and
// audit_log_retention tables, unqualified; [MigrationsInSchema] puts them
// in a chosen schema.
// Apply directly with the kit migrate helper:
//
//	migrate.Up(ctx, db, migrate.Config{
//	    Dir:     postgres.Migrations,
//	    Dialect: "postgres",
//	})
//
//go:embed migrations/*.sql
var Migrations embed.FS
