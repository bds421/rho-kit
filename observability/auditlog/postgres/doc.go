// Package postgres is the pgx-backed [auditlog.Store]. It implements
// the tamper-evident chain contract the in-process [auditlog.Logger]
// expects: AppendChained serialises every append under a per-store
// advisory lock, RangeChain iterates in monotonic append order
// independent of caller-supplied timestamps, and Query exposes the
// signed-cursor paginated view used by operator tooling.
//
// The module ships its own embedded [Migrations] so a service can
// apply the schema with the kit migrate helper without copying SQL.
// Integration tests live in the sibling integrationtest module so
// production callers do not transitively pull testcontainers.
package postgres
