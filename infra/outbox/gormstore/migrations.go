package gormstore

import "embed"

// Migrations contains the SQL migrations for the outbox_entries table.
// Use with goose or the kit migrate tool.
//
//go:embed migrations/*.sql
var Migrations embed.FS
