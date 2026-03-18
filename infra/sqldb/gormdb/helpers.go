package gormdb

import (
	"errors"

	"gorm.io/gorm"

	"github.com/bds421/rho-kit/core/apperror"
	"github.com/bds421/rho-kit/infra/sqldb"
)

// FindByID looks up a record by primary key.
// Returns apperror.NotFoundError if not found (caller expects the item to exist).
func FindByID[T any](db *gorm.DB, entityName, id string) (*T, error) {
	var record T
	err := db.Where("id = ?", id).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, apperror.NewNotFound(entityName, id)
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// FindOneByField looks up a single record by an arbitrary column.
// Returns (nil, nil) when not found — intentional for "check if exists" callers.
// Unlike FindByID (which returns apperror.NotFoundError), this function returns
// a nil pointer for missing records. Callers MUST nil-check the result before
// dereferencing.
func FindOneByField[T any](db *gorm.DB, field string, value any) (*T, error) {
	sqldb.ValidateColumn(field)
	var record T
	err := db.Where(map[string]any{field: value}).First(&record).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &record, nil
}

// CreateWithDefaultReset creates a record inside a transaction.
// If isDefault is true, it first clears is_default on all existing rows.
func CreateWithDefaultReset[T any](db *gorm.DB, record *T, isDefault bool) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if isDefault {
			if err := tx.Model(new(T)).Where("is_default = ?", true).Update("is_default", false).Error; err != nil {
				return err
			}
		}
		return tx.Create(record).Error
	})
}

// UpdateWithDefaultReset updates a record inside a transaction.
// If the updates include is_default=true, it first clears is_default on all
// other rows. Returns apperror.NotFoundError if no row was affected.
// Returns nil immediately if updates is empty (no-op).
//
// Safety: GORM's Updates(map) only writes columns matching the model schema,
// so arbitrary map keys are silently ignored rather than injected. Callers
// should still build the updates map from validated input, not raw user data.
func UpdateWithDefaultReset[T any](db *gorm.DB, entityName, id string, updates map[string]any) error {
	if len(updates) == 0 {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if isDefault, ok := updates["is_default"]; ok {
			if def, isBool := isDefault.(bool); isBool && def {
				if err := tx.Model(new(T)).Where("is_default = ? AND id != ?", true, id).Update("is_default", false).Error; err != nil {
					return err
				}
			}
		}

		result := tx.Model(new(T)).Where("id = ?", id).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return apperror.NewNotFound(entityName, id)
		}
		return nil
	})
}

// DeleteByID deletes a record by ID. Returns apperror.NotFoundError if no row
// was affected.
func DeleteByID[T any](db *gorm.DB, entityName, id string) error {
	result := db.Where("id = ?", id).Delete(new(T))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return apperror.NewNotFound(entityName, id)
	}
	return nil
}
