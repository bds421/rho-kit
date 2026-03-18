package gormpostgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/bds421/rho-kit/infra/sqldb"
)

// Compile-time interface check.
var _ sqldb.ErrorClassifier = Classifier{}

// Classifier implements [sqldb.ErrorClassifier] for PostgreSQL using
// native pgconn.PgError type assertions. This is more precise than the
// string-based fallback in the root sqldb package.
type Classifier struct{}

// IsDuplicateKey returns true for PostgreSQL error code 23505 (unique_violation).
func (Classifier) IsDuplicateKey(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return sqldb.IsDuplicateKeyError(err)
}

// IsForeignKey returns true for PostgreSQL error code 23503 (foreign_key_violation).
func (Classifier) IsForeignKey(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23503"
	}
	return sqldb.IsForeignKeyError(err)
}

// IsNotNull returns true for PostgreSQL error code 23502 (not_null_violation).
func (Classifier) IsNotNull(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23502"
	}
	return sqldb.IsNotNullError(err)
}

// IsSerialization returns true for PostgreSQL error code 40001 (serialization_failure).
func (Classifier) IsSerialization(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "40001"
	}
	return sqldb.IsSerializationError(err)
}
