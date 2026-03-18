package sqldb

import (
	"database/sql"
	"errors"
	"strings"
)

// ErrorClassifier provides driver-specific error classification.
// Each database provider package (gormdb/gormpostgres, gormdb/gormmysql)
// implements this interface using the native driver error types for
// precise matching without string-based heuristics.
//
// Services that want exact error classification should use the classifier
// returned by their provider package. The package-level Is*Error functions
// use string-based fallback that works without any driver import.
type ErrorClassifier interface {
	IsDuplicateKey(err error) bool
	IsForeignKey(err error) bool
	IsNotNull(err error) bool
	IsSerialization(err error) bool
}

// IsDuplicateKeyError returns true if the error represents a unique constraint
// violation. Uses string-based heuristics that work for both PostgreSQL and MySQL
// without importing driver packages.
//
// WARNING: String matching is locale-sensitive — non-English database installations
// may return different error messages. For production use, prefer the typed
// [ErrorClassifier] from gormdb/gormpostgres or gormdb/gormmysql which matches
// on native error codes (SQLSTATE / MySQL error numbers).
func IsDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "duplicate key") || strings.Contains(s, "Duplicate entry")
}

// IsForeignKeyError returns true if the error represents a foreign key
// constraint violation.
func IsForeignKeyError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "foreign key constraint")
}

// IsNotNullError returns true if the error represents a NOT NULL constraint
// violation.
func IsNotNullError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "not-null constraint")
}

// IsSerializationError returns true if the error represents a transaction
// serialization failure that should be retried.
func IsSerializationError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "serialization failure") || strings.Contains(s, "Deadlock found")
}

// IsNotFound returns true if the error is sql.ErrNoRows.
// Use this for unified "row not found" detection across repositories.
func IsNotFound(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
