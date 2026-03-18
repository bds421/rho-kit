// Package gormstore provides a GORM-backed [auditlog.Store] implementation.
//
// Events are stored in an "audit_events" table with indexes on timestamp,
// actor, action, and resource for efficient querying.
//
// The store implements [auditlog.RetentionStore] for use with
// [auditlog.RetentionJob] to clean up old events.
package gormstore
