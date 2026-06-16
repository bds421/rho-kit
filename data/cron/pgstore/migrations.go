package pgstore

import "embed"

// Migrations contains the SQL migrations for the cron_schedules table.
// Use with goose or the kit migrate tool so the schema ships compiled
// into the binary rather than out-of-band.
//
//go:embed migrations/*.sql
var Migrations embed.FS
