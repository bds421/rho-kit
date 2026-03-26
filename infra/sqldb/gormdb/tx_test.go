package gormdb

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestWithTx_Commit(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	err := WithTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Create(&testModel{ID: "1", Name: "alice"}).Error
	})
	require.NoError(t, err)

	result, err := FindByID[testModel](db, "test", "1")
	require.NoError(t, err)
	assert.Equal(t, "alice", result.Name)
}

func TestWithTx_RollbackOnError(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sentinel := errors.New("deliberate error")
	err := WithTx(ctx, db, func(tx *gorm.DB) error {
		_ = tx.Create(&testModel{ID: "1", Name: "alice"}).Error
		return sentinel
	})
	require.ErrorIs(t, err, sentinel)

	var count int64
	require.NoError(t, db.Model(&testModel{}).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestWithTx_RollbackOnPanic(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	assert.Panics(t, func() {
		_ = WithTx(ctx, db, func(tx *gorm.DB) error {
			_ = tx.Create(&testModel{ID: "1", Name: "alice"}).Error
			panic("boom")
		})
	})

	var count int64
	require.NoError(t, db.Model(&testModel{}).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestWithTxResult_ReturnsValue(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.Create(&testModel{ID: "1", Name: "alice"}).Error)

	result, err := WithTxResult(ctx, db, func(tx *gorm.DB) (*testModel, error) {
		return FindByID[testModel](tx, "test", "1")
	})
	require.NoError(t, err)
	assert.Equal(t, "alice", result.Name)
}

func TestWithTxResult_RollbackOnError(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	sentinel := errors.New("fail")
	result, err := WithTxResult(ctx, db, func(tx *gorm.DB) (*testModel, error) {
		return nil, sentinel
	})
	require.ErrorIs(t, err, sentinel)
	assert.Nil(t, result)
}

func TestWithTxResult_RollbackOnPanic(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	assert.Panics(t, func() {
		_, _ = WithTxResult(ctx, db, func(tx *gorm.DB) (*testModel, error) {
			_ = tx.Create(&testModel{ID: "1", Name: "alice"}).Error
			panic("boom")
		})
	})

	var count int64
	require.NoError(t, db.Model(&testModel{}).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

// WithReadOnlyTx success path requires PostgreSQL or MySQL 5.6.5+.
// SQLite does not support SET TRANSACTION READ ONLY, so only the
// error path is tested here. Run integration tests with -tags integration
// for full coverage.
func TestWithReadOnlyTx_SQLiteRejectsReadOnly(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	require.NoError(t, db.Create(&testModel{ID: "1", Name: "alice"}).Error)

	// SQLite does not support SET TRANSACTION READ ONLY, so we expect an error
	// from the SET command. This test verifies the function returns an error
	// rather than panicking or silently continuing.
	err := WithReadOnlyTx(ctx, db, func(tx *gorm.DB) error {
		var m testModel
		return tx.First(&m, "id = ?", "1").Error
	})
	// SQLite will reject SET TRANSACTION READ ONLY -- that's expected.
	// On a real PostgreSQL/MySQL instance this would succeed.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read-only")
}
