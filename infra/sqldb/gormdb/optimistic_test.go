package gormdb

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/bds421/rho-kit/core/apperror"
)

type versionedModel struct {
	ID      string `gorm:"primaryKey"`
	Name    string
	Version int64
}

func setupVersionedDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&versionedModel{}))
	return db
}

func TestCheckVersion_Success(t *testing.T) {
	db := setupVersionedDB(t)
	db.Create(&versionedModel{ID: "1", Name: "alice", Version: 1})

	model := &versionedModel{ID: "1"}
	err := CheckVersion(db, model, 1)
	require.NoError(t, err)

	var updated versionedModel
	db.First(&updated, "id = ?", "1")
	assert.Equal(t, int64(2), updated.Version)
}

func TestCheckVersion_Conflict(t *testing.T) {
	db := setupVersionedDB(t)
	db.Create(&versionedModel{ID: "1", Name: "alice", Version: 3})

	model := &versionedModel{ID: "1"}
	err := CheckVersion(db, model, 1)
	require.ErrorIs(t, err, ErrVersionConflict)

	// Verify version was not changed.
	var unchanged versionedModel
	db.First(&unchanged, "id = ?", "1")
	assert.Equal(t, int64(3), unchanged.Version)
}

func TestCheckVersion_NonExistentRow(t *testing.T) {
	db := setupVersionedDB(t)

	model := &versionedModel{ID: "nonexistent"}
	err := CheckVersion(db, model, 1)
	require.ErrorIs(t, err, ErrVersionConflict)
	assert.True(t, apperror.IsConflict(err))
}

func TestCheckVersion_ErrVersionConflictIsConflict(t *testing.T) {
	assert.True(t, apperror.IsConflict(ErrVersionConflict))
}
