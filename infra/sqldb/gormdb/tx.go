package gormdb

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// WithTx runs fn inside a database transaction. It commits on nil error and
// rolls back on error or panic. If fn panics, the transaction is rolled back
// and the panic is re-raised after cleanup.
func WithTx(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	tx := db.WithContext(ctx).Begin()
	if tx.Error != nil {
		return tx.Error
	}

	panicked := true
	defer func() {
		if panicked {
			_ = tx.Rollback().Error
		}
	}()

	if err := fn(tx); err != nil {
		_ = tx.Rollback().Error
		panicked = false
		return err
	}

	panicked = false
	return tx.Commit().Error
}

// WithTxResult runs fn inside a transaction and returns the result. It commits
// on nil error and rolls back on error or panic, same as [WithTx].
func WithTxResult[T any](ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) (T, error)) (T, error) {
	var result T
	err := WithTx(ctx, db, func(tx *gorm.DB) error {
		var fnErr error
		result, fnErr = fn(tx)
		return fnErr
	})
	return result, err
}

// WithReadOnlyTx runs fn inside a read-only transaction. After beginning the
// transaction it issues SET TRANSACTION READ ONLY, which is supported by
// PostgreSQL and MySQL 5.6.5+. The transaction is committed or rolled back
// using the same semantics as [WithTx].
func WithReadOnlyTx(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	return WithTx(ctx, db, func(tx *gorm.DB) error {
		if err := tx.Exec("SET TRANSACTION READ ONLY").Error; err != nil {
			return fmt.Errorf("gormdb: set read-only: %w", err)
		}
		return fn(tx)
	})
}
