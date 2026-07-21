package pgstore

import "embed"

// Migrations contains the SQL migrations for the saga_instances table.
// Embed this in your service's migration set or use it directly with the
// kit migrate tool:
//
//	migrate.Up(ctx, db, migrate.Config{
//	    Dir:     pgstore.Migrations,
//	    Dialect: "postgres",
//	})
//
//go:embed migrations/*.sql
var Migrations embed.FS
