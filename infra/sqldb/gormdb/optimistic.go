package gormdb

import (
	"fmt"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/core/apperror"
)

// ErrVersionConflict is returned when an optimistic locking check fails.
var ErrVersionConflict = apperror.NewConflict("version conflict: row was modified by another transaction")

// CheckVersion increments the version column only if it matches expectedVersion.
// Use this to detect concurrent modifications without changing other fields.
// For updating fields with optimistic locking, use [UpdateWithVersion] instead.
//
// Returns [ErrVersionConflict] if no rows were affected (the row was modified
// by another transaction or does not exist). Any other database error is
// wrapped and returned.
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

// UpdateWithVersion atomically updates the model's fields only if the current
// version matches expectedVersion. On success, the version column is incremented.
// Returns [ErrVersionConflict] if the version does not match (row was modified by
// another transaction or does not exist).
//
// The updates parameter is a map[string]any of column names to new values.
// The version field is automatically set to expectedVersion+1 — do not include
// it in updates.
//
// The input updates map is never mutated; a shallow copy is used internally.
//
// Note: if the row identified by model's primary key does not exist,
// RowsAffected will be 0, and ErrVersionConflict is returned. Callers
// that need to distinguish "deleted" from "stale version" should check
// existence separately.
func UpdateWithVersion(db *gorm.DB, model any, expectedVersion int64, updates map[string]any) error {
	if len(updates) == 0 {
		return fmt.Errorf("gormdb: updates must not be empty")
	}
	if _, ok := updates["version"]; ok {
		return fmt.Errorf("gormdb: updates must not contain \"version\"; it is managed automatically by UpdateWithVersion")
	}

	merged := make(map[string]any, len(updates)+1)
	for k, v := range updates {
		merged[k] = v
	}
	merged["version"] = expectedVersion + 1

	result := db.Model(model).
		Where("version = ?", expectedVersion).
		Updates(merged)

	if result.Error != nil {
		return fmt.Errorf("gormdb: update with version: %w", result.Error)
	}

	if result.RowsAffected == 0 {
		return ErrVersionConflict
	}

	return nil
}
