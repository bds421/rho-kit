package postgres

import "embed"

// Migrations contains the SQL migrations for the co-published outbox_entries
// and inbox_entries tables.
// Apply directly with the kit migrate helper:
//
//	migrate.Up(ctx, db, migrate.Config{
//	    Dir:     postgres.Migrations,
//	    Dialect: "postgres",
//	})
//
//go:embed migrations/*.sql
var Migrations embed.FS
