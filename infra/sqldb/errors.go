package sqldb

import (
	"database/sql"
	"errors"
	"strings"
)

// ErrorClassifier provides PostgreSQL-specific error classification.
// The pgx adapter implements this interface using native pgconn.PgError
// SQLSTATE codes for precise matching without string-based heuristics.
//
// Services that want exact error classification should obtain the
// classifier from their pgx provider. The package-level Is*Error
// functions use string-based fallback that works without any driver
// import — useful for tests and for callers wrapping pgx errors at
// the boundary.
type ErrorClassifier interface {
	IsDuplicateKey(err error) bool
	IsForeignKey(err error) bool
	IsNotNull(err error) bool
	IsSerialization(err error) bool
}

// IsDuplicateKeyError returns true if the error represents a unique
// constraint violation. Uses string-based heuristics so it works
// without importing pgx.
//
// WARNING: String matching is locale-sensitive — non-English Postgres
// installations may return different error messages. For production
// use, prefer the typed [ErrorClassifier] from the pgx provider which
// matches on SQLSTATE codes (23505 for unique violation).
func IsDuplicateKeyError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "duplicate key")
}

// IsForeignKeyError returns true if the error represents a foreign key
// constraint violation.
func IsForeignKeyError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "foreign key constraint")
}

// IsNotNullError returns true if the error represents a NOT NULL
// constraint violation.
func IsNotNullError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "not-null constraint")
}

// IsSerializationError returns true if the error represents a
// transaction serialization failure that should be retried
// (Postgres SQLSTATE 40001).
//
// Matches common libpq/pgx message shapes without importing the driver:
// "serialization failure", "could not serialize access", and the
// SQLSTATE token "40001". For precise code matching prefer the pgx
// [ErrorClassifier].
func IsSerializationError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "serialization failure") ||
		strings.Contains(msg, "could not serialize access") ||
		strings.Contains(msg, "40001")
}

// IsNotFound returns true if the error is a "no rows" sentinel.
// Matches [sql.ErrNoRows] and the common pgx message shape so callers
// on the canonical pgx path do not need a separate branch. Prefer
// errors.Is against the concrete sentinel when available.
func IsNotFound(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	// pgx.ErrNoRows has the same Error() string as database/sql when
	// the adapter does not wrap with errors.Is-compatible sql.ErrNoRows.
	msg := err.Error()
	return msg == "no rows in result set" || strings.Contains(strings.ToLower(msg), "no rows in result set")
}
