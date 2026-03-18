package pgstore

import "embed"

// Migrations contains the SQL migrations for the idempotency_keys table.
// Use with goose or the kit migrate tool.
//
//go:embed migrations/*.sql
var Migrations embed.FS
