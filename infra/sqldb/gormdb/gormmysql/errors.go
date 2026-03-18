package gormmysql

import (
	"errors"

	"github.com/go-sql-driver/mysql"

	"github.com/bds421/rho-kit/infra/sqldb"
)

// Compile-time interface check.
var _ sqldb.ErrorClassifier = Classifier{}

// Classifier implements [sqldb.ErrorClassifier] for MySQL/MariaDB using
// native mysql.MySQLError type assertions. This is more precise than the
// string-based fallback in the root sqldb package.
type Classifier struct{}

// IsDuplicateKey returns true for MySQL error 1062 (ER_DUP_ENTRY).
func (Classifier) IsDuplicateKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1062
	}
	return sqldb.IsDuplicateKeyError(err)
}

// IsForeignKey returns true for MySQL error 1452 (ER_NO_REFERENCED_ROW_2).
func (Classifier) IsForeignKey(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1452
	}
	return sqldb.IsForeignKeyError(err)
}

// IsNotNull returns true for MySQL error 1048 (ER_BAD_NULL_ERROR).
func (Classifier) IsNotNull(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1048
	}
	return sqldb.IsNotNullError(err)
}

// IsSerialization returns true for MySQL error 1213 (ER_LOCK_DEADLOCK).
func (Classifier) IsSerialization(err error) bool {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return mysqlErr.Number == 1213
	}
	return sqldb.IsSerializationError(err)
}
