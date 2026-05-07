package postgres

import "embed"

// Migrations contains the SQL migrations for the approval_requests
// table. Embed in your service's migration set or run via the kit
// migrate tool.
//
//go:embed migrations/*.sql
var Migrations embed.FS
