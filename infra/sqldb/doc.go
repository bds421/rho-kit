// Package sqldb is the kit's PostgreSQL surface: connection config,
// pool tuning, error classification, schema-safe column quoting, and
// the [Pinger] interface used by the readiness probe.
//
// v2 dropped MySQL/MariaDB and GORM. The data path is now pgx (driver)
// + sqlc (typed query generation) + goose (migrations). Heavy SDK
// adapters live in infra/sqldb/pgx.
package sqldb
