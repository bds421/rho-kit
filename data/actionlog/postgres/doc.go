// Package postgres provides a pgx-backed [actionlog.Store] for the
// kit-canonical Postgres deployment.
//
// Schema lives in [Migrations]; embed it into the service's migration
// set or run via the kit migrate tool. The store assumes the schema is
// in place — it does not generate schema at runtime.
//
// The store runs hand-written pgx queries so the append path can use
// Postgres transactions, row locks, and advisory locks directly.
package postgres
