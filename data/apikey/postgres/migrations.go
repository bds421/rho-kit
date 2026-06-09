package postgres

import "embed"

// Migrations contains the SQL migrations for the api_keys table. Embed this
// in your service's migration set or use it directly with the kit migrate
// tool:
//
//	migrate.Up(ctx, db, migrate.Config{
//	    Dir:     postgres.Migrations,
//	    Dialect: "postgres",
//	})
//
//go:embed migrations/*.sql
var Migrations embed.FS
