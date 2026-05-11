// Package postgres provides a pgx-backed [approval.Store] for the
// kit-canonical Postgres deployment.
//
// State transitions go through a single SELECT-FOR-UPDATE + UPDATE
// inside a Postgres transaction so concurrent approvers can't both flip
// the row to inconsistent terminal states.
package postgres
