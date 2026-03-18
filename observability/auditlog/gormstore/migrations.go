package gormstore

import "embed"

// Migrations contains the SQL migration files for the audit_events table.
// Embed this in your service's migration set or use it directly with
// [migrate.Up]:
//
//	migrate.Up(ctx, db, migrate.Config{
//	    Dir:     gormstore.Migrations,
//	    Dialect: "postgres",
//	})
//
//go:embed migrations/*.sql
var Migrations embed.FS
