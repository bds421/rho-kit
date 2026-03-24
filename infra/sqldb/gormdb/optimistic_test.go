package gormdb

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/apperror"
)

type versionedModel struct {
	ID      string `gorm:"primaryKey"`
	Name    string
	Version int64
}

func TestCheckVersion_Success(t *testing.T) {
	db := setupTestDB(t, &versionedModel{})
	require.NoError(t, db.Create(&versionedModel{ID: "1", Name: "alice", Version: 1}).Error)

	model := &versionedModel{ID: "1"}
	err := CheckVersion(context.Background(), db, model, 1)
	require.NoError(t, err)

	var updated versionedModel
	db.First(&updated, "id = ?", "1")
	assert.Equal(t, int64(2), updated.Version)
}

func TestCheckVersion_Conflict(t *testing.T) {
	db := setupTestDB(t, &versionedModel{})
	require.NoError(t, db.Create(&versionedModel{ID: "1", Name: "alice", Version: 3}).Error)

	model := &versionedModel{ID: "1"}
	err := CheckVersion(context.Background(), db, model, 1)
	require.ErrorIs(t, err, ErrVersionConflict)

	// Verify version was not changed.
	var unchanged versionedModel
	db.First(&unchanged, "id = ?", "1")
	assert.Equal(t, int64(3), unchanged.Version)
}

func TestCheckVersion_NonExistentRow(t *testing.T) {
	db := setupTestDB(t, &versionedModel{})

	model := &versionedModel{ID: "nonexistent"}
	err := CheckVersion(context.Background(), db, model, 1)
	require.ErrorIs(t, err, ErrVersionConflict)
	assert.True(t, apperror.IsConflict(err))
}

func TestCheckVersion_ErrVersionConflictIsConflict(t *testing.T) {
	assert.True(t, apperror.IsConflict(ErrVersionConflict))
}

func TestUpdateWithVersion_Success(t *testing.T) {
	db := setupTestDB(t, &versionedModel{})
	require.NoError(t, db.Create(&versionedModel{ID: "1", Name: "alice", Version: 1}).Error)

	model := &versionedModel{ID: "1"}
	err := UpdateWithVersion(context.Background(), db, model, 1, map[string]any{"name": "bob"})
	require.NoError(t, err)

	var updated versionedModel
	db.First(&updated, "id = ?", "1")
	assert.Equal(t, "bob", updated.Name)
	assert.Equal(t, int64(2), updated.Version)
}

func TestUpdateWithVersion_Conflict(t *testing.T) {
	db := setupTestDB(t, &versionedModel{})
	require.NoError(t, db.Create(&versionedModel{ID: "1", Name: "alice", Version: 3}).Error)

	model := &versionedModel{ID: "1"}
	err := UpdateWithVersion(context.Background(), db, model, 1, map[string]any{"name": "bob"})
	require.ErrorIs(t, err, ErrVersionConflict)

	// Verify neither name nor version changed.
	var unchanged versionedModel
	db.First(&unchanged, "id = ?", "1")
	assert.Equal(t, "alice", unchanged.Name)
	assert.Equal(t, int64(3), unchanged.Version)
}

func TestUpdateWithVersion_NonExistentRow(t *testing.T) {
	db := setupTestDB(t, &versionedModel{})

	model := &versionedModel{ID: "nonexistent"}
	err := UpdateWithVersion(context.Background(), db, model, 1, map[string]any{"name": "bob"})
	require.ErrorIs(t, err, ErrVersionConflict)
	assert.True(t, apperror.IsConflict(err))
}

func TestUpdateWithVersion_DoesNotMutateInput(t *testing.T) {
	db := setupTestDB(t, &versionedModel{})
	require.NoError(t, db.Create(&versionedModel{ID: "1", Name: "alice", Version: 1}).Error)

	updates := map[string]any{"name": "bob"}
	model := &versionedModel{ID: "1"}
	err := UpdateWithVersion(context.Background(), db, model, 1, updates)
	require.NoError(t, err)

	// The original map must not contain the injected "version" key.
	_, hasVersion := updates["version"]
	assert.False(t, hasVersion, "input updates map was mutated")
	assert.Len(t, updates, 1)
}

func TestUpdateWithVersion_EmptyUpdatesReturnsError(t *testing.T) {
	db := setupTestDB(t, &versionedModel{})
	require.NoError(t, db.Create(&versionedModel{ID: "1", Name: "alice", Version: 1}).Error)

	model := &versionedModel{ID: "1"}

	t.Run("nil map", func(t *testing.T) {
		err := UpdateWithVersion(context.Background(), db, model, 1, nil)
		require.ErrorIs(t, err, ErrEmptyUpdates)

		// Verify DB unchanged.
		var row versionedModel
		db.First(&row, "id = ?", "1")
		assert.Equal(t, "alice", row.Name)
		assert.Equal(t, int64(1), row.Version)
	})

	t.Run("empty map", func(t *testing.T) {
		err := UpdateWithVersion(context.Background(), db, model, 1, map[string]any{})
		require.ErrorIs(t, err, ErrEmptyUpdates)

		// Verify DB unchanged.
		var row versionedModel
		db.First(&row, "id = ?", "1")
		assert.Equal(t, "alice", row.Name)
		assert.Equal(t, int64(1), row.Version)
	})
}

func TestUpdateWithVersion_RejectsVersionKey(t *testing.T) {
	db := setupTestDB(t, &versionedModel{})
	require.NoError(t, db.Create(&versionedModel{ID: "1", Name: "alice", Version: 1}).Error)

	model := &versionedModel{ID: "1"}

	t.Run("lowercase", func(t *testing.T) {
		err := UpdateWithVersion(context.Background(), db, model, 1, map[string]any{"name": "bob", "version": int64(99)})
		require.ErrorIs(t, err, ErrVersionKeyInUpdates)

		// Verify DB unchanged.
		var row versionedModel
		db.First(&row, "id = ?", "1")
		assert.Equal(t, "alice", row.Name)
		assert.Equal(t, int64(1), row.Version)
	})

	t.Run("mixed case", func(t *testing.T) {
		err := UpdateWithVersion(context.Background(), db, model, 1, map[string]any{"name": "bob", "Version": int64(99)})
		require.ErrorIs(t, err, ErrVersionKeyInUpdates)

		// Verify DB unchanged.
		var row versionedModel
		db.First(&row, "id = ?", "1")
		assert.Equal(t, "alice", row.Name)
		assert.Equal(t, int64(1), row.Version)
	})

	t.Run("upper case", func(t *testing.T) {
		err := UpdateWithVersion(context.Background(), db, model, 1, map[string]any{"name": "bob", "VERSION": int64(99)})
		require.ErrorIs(t, err, ErrVersionKeyInUpdates)

		// Verify DB unchanged.
		var row versionedModel
		db.First(&row, "id = ?", "1")
		assert.Equal(t, "alice", row.Name)
		assert.Equal(t, int64(1), row.Version)
	})
}

func TestCheckVersion_NilModelReturnsError(t *testing.T) {
	db := setupTestDB(t, &versionedModel{})

	err := CheckVersion(context.Background(), db, nil, 1)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNilModel))
}

func TestUpdateWithVersion_NilModelReturnsError(t *testing.T) {
	db := setupTestDB(t, &versionedModel{})

	err := UpdateWithVersion(context.Background(), db, nil, 1, map[string]any{"name": "bob"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNilModel))
}
