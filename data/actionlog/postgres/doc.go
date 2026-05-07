// Package postgres provides a GORM-backed [actionlog.Store] for the
// kit-canonical Postgres deployment.
//
// Schema lives in [Migrations]; embed it into the service's migration
// set or run via the kit migrate tool. The store assumes the schema is
// in place — it does not run AutoMigrate.
//
// Why GORM and not raw SQL? The kit's other relational stores
// (idempotency/pgstore, observability/auditlog/gormstore, outbox/
// gormstore) are split: idempotency runs raw because it leans on
// dialect-specific upsert; auditlog/outbox use GORM because their
// access pattern is simple inserts + scoped reads. actionlog falls
// firmly in the second bucket — append, get, list with a few filters
// — so it follows the GORM precedent for cross-dialect portability
// (sqlite for tests via memdb, postgres in prod).
package postgres
