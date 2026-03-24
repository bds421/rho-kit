package gormdb

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/core/apperror"
)

// ErrVersionConflict is returned when an optimistic locking check fails.
var ErrVersionConflict = apperror.NewConflict("version conflict: row was modified by another transaction")

// CheckVersion updates the model only if the current version matches
// expectedVersion. On success the version column is incremented by one.
// Returns [ErrVersionConflict] if no rows were affected (the row was modified
// by another transaction). Any other database error is wrapped and returned.
//
// Note: if the row identified by model's primary key does not exist,
// RowsAffected will be 0, and ErrVersionConflict is returned. Callers
// that need to distinguish "deleted" from "stale version" should check
// existence separately.
func CheckVersion(db *gorm.DB, model any, expectedVersion int64) error {
	result := db.Model(model).
		Where("version = ?", expectedVersion).
		Update("version", expectedVersion+1)

	if result.Error != nil {
		return fmt.Errorf("gormdb: check version: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return ErrVersionConflict
	}

	return nil
}
