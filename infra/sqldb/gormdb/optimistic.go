package gormdb

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// ErrVersionConflict is returned when an optimistic locking check fails.
var ErrVersionConflict = errors.New("gormdb: version conflict (row was modified by another transaction)")

// CheckVersion updates the model only if the current version matches
// expectedVersion. On success the version column is incremented by one.
// Returns [ErrVersionConflict] if no rows were affected (the row was modified
// by another transaction). Any other database error is wrapped and returned.
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
