package gormdb

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/bds421/rho-kit/core/apperror"
)

type testModel struct {
	ID        string `gorm:"primaryKey"`
	Name      string
	IsDefault bool
}

func setupTestDB(t *testing.T, models ...any) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	if len(models) == 0 {
		models = []any{&testModel{}}
	}
	require.NoError(t, db.AutoMigrate(models...))
	return db
}

func TestFindByID_Found(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.Create(&testModel{ID: "1", Name: "alice"}).Error)

	result, err := FindByID[testModel](db, "test", "1")
	require.NoError(t, err)
	assert.Equal(t, "alice", result.Name)
}

func TestFindByID_NotFound(t *testing.T) {
	db := setupTestDB(t)

	_, err := FindByID[testModel](db, "test", "missing")
	assert.True(t, apperror.IsNotFound(err))
}

func TestFindOneByField_Found(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.Create(&testModel{ID: "1", Name: "alice"}).Error)

	result, err := FindOneByField[testModel](db, "name", "alice")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "1", result.ID)
}

// testReservedWordModel has a column named "key" which is a MySQL/SQLite reserved word.
type testReservedWordModel struct {
	ID  string `gorm:"primaryKey"`
	Key string
}

func TestFindOneByField_ReservedWordColumn(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&testReservedWordModel{}))

	require.NoError(t, db.Create(&testReservedWordModel{ID: "1", Key: "my-flag"}).Error)

	result, err := FindOneByField[testReservedWordModel](db, "key", "my-flag")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "1", result.ID)
}

func TestFindOneByField_NotFound(t *testing.T) {
	db := setupTestDB(t)

	result, err := FindOneByField[testModel](db, "name", "nobody")
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestCreateWithDefaultReset_SetsDefault(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.Create(&testModel{ID: "1", Name: "first", IsDefault: true}).Error)

	record := &testModel{ID: "2", Name: "second", IsDefault: true}
	require.NoError(t, CreateWithDefaultReset(db, record, true))

	var first testModel
	db.First(&first, "id = ?", "1")
	assert.False(t, first.IsDefault)

	var second testModel
	db.First(&second, "id = ?", "2")
	assert.True(t, second.IsDefault)
}

func TestCreateWithDefaultReset_NoDefault(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.Create(&testModel{ID: "1", Name: "first", IsDefault: true}).Error)

	record := &testModel{ID: "2", Name: "second", IsDefault: false}
	require.NoError(t, CreateWithDefaultReset(db, record, false))

	var first testModel
	db.First(&first, "id = ?", "1")
	assert.True(t, first.IsDefault)
}

func TestUpdateWithDefaultReset_PromotesDefault(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.Create(&testModel{ID: "1", Name: "first", IsDefault: true}).Error)
	require.NoError(t, db.Create(&testModel{ID: "2", Name: "second", IsDefault: false}).Error)

	err := UpdateWithDefaultReset[testModel](db, "test", "2", map[string]any{"is_default": true})
	require.NoError(t, err)

	var first testModel
	db.First(&first, "id = ?", "1")
	assert.False(t, first.IsDefault)

	var second testModel
	db.First(&second, "id = ?", "2")
	assert.True(t, second.IsDefault)
}

func TestUpdateWithDefaultReset_SetDefaultFalse(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.Create(&testModel{ID: "1", Name: "first", IsDefault: true}).Error)

	err := UpdateWithDefaultReset[testModel](db, "test", "1", map[string]any{"is_default": false})
	require.NoError(t, err)

	var first testModel
	db.First(&first, "id = ?", "1")
	assert.False(t, first.IsDefault)
}

func TestUpdateWithDefaultReset_NonBoolDefault(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.Create(&testModel{ID: "1", Name: "first", IsDefault: false}).Error)

	err := UpdateWithDefaultReset[testModel](db, "test", "1", map[string]any{"is_default": "yes", "name": "updated"})
	require.NoError(t, err)

	var first testModel
	db.First(&first, "id = ?", "1")
	assert.Equal(t, "updated", first.Name)
}

func TestUpdateWithDefaultReset_NotFound(t *testing.T) {
	db := setupTestDB(t)

	err := UpdateWithDefaultReset[testModel](db, "test", "missing", map[string]any{"name": "foo"})
	assert.True(t, apperror.IsNotFound(err))
}

func TestDeleteByID_Found(t *testing.T) {
	db := setupTestDB(t)
	require.NoError(t, db.Create(&testModel{ID: "1", Name: "alice"}).Error)

	require.NoError(t, DeleteByID[testModel](db, "test", "1"))

	var count int64
	db.Model(&testModel{}).Count(&count)
	assert.Equal(t, int64(0), count)
}

func TestDeleteByID_NotFound(t *testing.T) {
	db := setupTestDB(t)

	err := DeleteByID[testModel](db, "test", "missing")
	assert.True(t, apperror.IsNotFound(err))
}
