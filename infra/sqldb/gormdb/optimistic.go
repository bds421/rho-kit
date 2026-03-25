package gormdb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/core/apperror"
)

// ErrVersionConflict is returned when an optimistic locking check fails.
var ErrVersionConflict = apperror.NewConflict("version conflict: row was modified by another transaction")

// ErrNilModel is returned when a nil model is passed to UpdateWithVersion.
var ErrNilModel = errors.New("gormdb: model must not be nil")

// ErrEmptyUpdates is returned when UpdateWithVersion is called with a nil or empty updates map.
var ErrEmptyUpdates = errors.New("gormdb: updates must not be empty")

// ErrVersionKeyInUpdates is returned when the updates map passed to UpdateWithVersion
// contains a "version" key, which is managed automatically.
var ErrVersionKeyInUpdates = errors.New("gormdb: updates must not contain \"version\"; it is managed automatically by UpdateWithVersion")

// UpdateWithVersion atomically updates the model's fields only if the current
// version matches expectedVersion. On success, the version column is incremented.
// Returns [ErrVersionConflict] if the version does not match (row was modified by
// another transaction or does not exist).
//
// On success, the database row is updated but the in-memory model struct is not
// refreshed. Re-read the row if you need the updated values.
//
// The updates parameter is a map[string]any of column names to new values.
// The version field is automatically set to expectedVersion+1 -- do not include
// it in updates.
//
// The input updates map is never mutated; a shallow copy is used internally.
//
// The caller is responsible for ensuring expectedVersion does not overflow int64.
//
// Both the model and the database table must have a column named "version".
// Custom column names are not supported. The model's struct must include a field
// mapped to that column (e.g., Version int64 or Version int64 `gorm:"column:version"`).
//
// Note: if the row identified by model's primary key does not exist,
// RowsAffected will be 0, and ErrVersionConflict is returned. Callers
// that need to distinguish "deleted" from "stale version" should check
// existence separately.
func UpdateWithVersion(ctx context.Context, db *gorm.DB, model any, expectedVersion int64, updates map[string]any) error {
	if model == nil {
		return ErrNilModel
	}

	if len(updates) == 0 {
		return ErrEmptyUpdates
	}
	for k := range updates {
		if strings.EqualFold(k, "version") {
			return ErrVersionKeyInUpdates
		}
	}

	merged := make(map[string]any, len(updates)+1)
	for k, v := range updates {
		merged[k] = v
	}
	merged["version"] = expectedVersion + 1

	result := db.WithContext(ctx).Model(model).
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
