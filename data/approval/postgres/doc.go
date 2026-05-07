// Package postgres provides a GORM-backed [approval.Store] for the
// kit-canonical Postgres deployment.
//
// State transitions go through a single SELECT-FOR-UPDATE + UPDATE
// inside a GORM transaction so concurrent approvers can't both flip
// the row to inconsistent terminal states. Sqlite (used by tests via
// memdb) doesn't support FOR UPDATE — GORM elides the clause on that
// dialect, and the test runs serially anyway, so the difference is
// invisible.
package postgres
