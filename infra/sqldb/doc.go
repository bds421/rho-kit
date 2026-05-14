// Package sqldb is the kit's PostgreSQL surface: connection config,
// pool tuning, error classification, schema-safe column quoting, and
// the [Pinger] interface used by the readiness probe.
//
// The data path is pgx (driver) + sqlc (typed query generation) +
// goose (migrations). Heavy SDK adapters live in infra/sqldb/pgx.
package sqldb
