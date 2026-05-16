// Package tenant provides per-tenant data-isolation primitives that
// sit between [core/tenant.ID] (the type) and a concrete data
// backend (pgxbackend, rediscache, etc.).
//
// The kit's standing rule is that every multi-tenant data access
// must scope to a single tenant.ID — forgetting WHERE tenant_id=?
// (or its analogue) is one of the kit's most expensive footguns.
// Wave 148 introduced this package as the seam where that
// invariant can be enforced once, rather than re-built in every
// service.
//
// # What this package provides
//
//   - [Scope]: a value type pairing a [coretenant.ID] with the
//     small amount of state needed to inject the tenant filter
//     into a backend call. Construct via [NewScope] (validates the
//     ID) or [FromContext] (extracts the ID from the request
//     context and rejects anonymous calls).
//   - [Scope.WhereClause]: returns a SQL fragment plus the
//     positional arg, given the current placeholder count, so
//     callers compose tenant filters into hand-written queries
//     without manual placeholder bookkeeping. Postgres-shaped
//     ($N) by default — adapters in pgxbackend wire this into
//     their query builders.
//   - [Scope.Key]: prefixes a cache/idempotency/lock key with the
//     tenant ID using the same safe-character contract as
//     [coretenant.KeyFor], so the result is collision-free across
//     tenants without each backend reinventing the prefix logic.
//
// # What this package deliberately does NOT do
//
// The package does not wrap *sql.DB or pgxpool.Pool — those are
// adapter-specific and a generic wrapper would either leak the
// backend type into kit-wide code or duplicate every method. The
// kit's recommended pattern is for each backend (e.g.
// data/budget/postgres) to expose a Scoped(*pgxpool.Pool, Scope)
// constructor that takes the Scope explicitly. Forgetting the
// Scope at call time becomes a compile-time error — the
// constructor refuses to compile without it.
//
// Wave 148 ships the Scope type + tests; future waves migrate
// individual data adapters to require it at construction time.
package tenant
