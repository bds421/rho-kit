package gormdb

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type cursorTestItem struct {
	ID      string `gorm:"primaryKey"`
	Name    string
	Enabled bool
}

func setupCursorDB(t *testing.T, items ...cursorTestItem) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&cursorTestItem{}))
	if len(items) > 0 {
		require.NoError(t, db.Create(&items).Error)
	}
	return db
}

func TestCursorQuery_SearchLike(t *testing.T) {
	db := setupCursorDB(t,
		cursorTestItem{ID: "3", Name: "alpha"},
		cursorTestItem{ID: "2", Name: "beta"},
		cursorTestItem{ID: "1", Name: "gamma"},
	)

	var results []cursorTestItem
	q := NewCursorQuery(db.Model(&cursorTestItem{})).SearchLike("alph", "name")
	err := q.Desc(10).Find(&results)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "alpha", results[0].Name)
}

func TestCursorQuery_SearchLike_Empty(t *testing.T) {
	db := setupCursorDB(t,
		cursorTestItem{ID: "2", Name: "alpha"},
		cursorTestItem{ID: "1", Name: "beta"},
	)

	var results []cursorTestItem
	q := NewCursorQuery(db.Model(&cursorTestItem{})).SearchLike("", "name")
	err := q.Desc(10).Find(&results)
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestCursorQuery_SearchLike_MultipleColumns(t *testing.T) {
	db := setupCursorDB(t,
		cursorTestItem{ID: "2", Name: "foo"},
		cursorTestItem{ID: "1", Name: "bar"},
	)

	var results []cursorTestItem
	q := NewCursorQuery(db.Model(&cursorTestItem{})).SearchLike("bar", "id", "name")
	err := q.Desc(10).Find(&results)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "bar", results[0].Name)
}

func TestWherePtr_NilSkipped(t *testing.T) {
	db := setupCursorDB(t,
		cursorTestItem{ID: "2", Name: "a", Enabled: true},
		cursorTestItem{ID: "1", Name: "b", Enabled: false},
	)

	var results []cursorTestItem
	q := NewCursorQuery(db.Model(&cursorTestItem{}))
	q = WherePtr(q, "enabled", (*bool)(nil))
	err := q.Desc(10).Find(&results)
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestWherePtr_FilterApplied(t *testing.T) {
	db := setupCursorDB(t,
		cursorTestItem{ID: "2", Name: "a", Enabled: true},
		cursorTestItem{ID: "1", Name: "b", Enabled: false},
	)

	enabled := true
	var results []cursorTestItem
	q := NewCursorQuery(db.Model(&cursorTestItem{}))
	q = WherePtr(q, "enabled", &enabled)
	err := q.Desc(10).Find(&results)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "a", results[0].Name)
}

func TestCursorQuery_Cursor(t *testing.T) {
	db := setupCursorDB(t,
		cursorTestItem{ID: "3", Name: "c"},
		cursorTestItem{ID: "2", Name: "b"},
		cursorTestItem{ID: "1", Name: "a"},
	)

	var results []cursorTestItem
	q := NewCursorQuery(db.Model(&cursorTestItem{})).Cursor("3")
	err := q.Desc(10).Find(&results)
	require.NoError(t, err)
	assert.Len(t, results, 2)
	assert.Equal(t, "2", results[0].ID)
	assert.Equal(t, "1", results[1].ID)
}

func TestCursorQuery_Cursor_Empty(t *testing.T) {
	db := setupCursorDB(t,
		cursorTestItem{ID: "2", Name: "b"},
		cursorTestItem{ID: "1", Name: "a"},
	)

	var results []cursorTestItem
	q := NewCursorQuery(db.Model(&cursorTestItem{})).Cursor("")
	err := q.Desc(10).Find(&results)
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestCursorQuery_Desc_LimitPlusOne(t *testing.T) {
	db := setupCursorDB(t,
		cursorTestItem{ID: "3", Name: "c"},
		cursorTestItem{ID: "2", Name: "b"},
		cursorTestItem{ID: "1", Name: "a"},
	)

	var results []cursorTestItem
	q := NewCursorQuery(db.Model(&cursorTestItem{}))
	err := q.Desc(2).Find(&results)
	require.NoError(t, err)
	assert.Len(t, results, 3)
	assert.Equal(t, "3", results[0].ID)
}

func TestCursorQuery_Where(t *testing.T) {
	db := setupCursorDB(t,
		cursorTestItem{ID: "2", Name: "a", Enabled: true},
		cursorTestItem{ID: "1", Name: "b", Enabled: false},
	)

	var results []cursorTestItem
	q := NewCursorQuery(db.Model(&cursorTestItem{})).Where("enabled", true)
	err := q.Desc(10).Find(&results)
	require.NoError(t, err)
	assert.Len(t, results, 1)
	assert.Equal(t, "a", results[0].Name)
}

func TestCursorQuery_Where_NilSkipped(t *testing.T) {
	db := setupCursorDB(t,
		cursorTestItem{ID: "2", Name: "a"},
		cursorTestItem{ID: "1", Name: "b"},
	)

	var results []cursorTestItem
	q := NewCursorQuery(db.Model(&cursorTestItem{})).Where("enabled", nil)
	err := q.Desc(10).Find(&results)
	require.NoError(t, err)
	assert.Len(t, results, 2)
}

func TestCursorQuery_WithTable(t *testing.T) {
	db := setupCursorDB(t,
		cursorTestItem{ID: "3", Name: "c"},
		cursorTestItem{ID: "2", Name: "b"},
		cursorTestItem{ID: "1", Name: "a"},
	)

	var results []cursorTestItem
	q := NewCursorQuery(db.Model(&cursorTestItem{})).WithTable("cursor_test_items")
	err := q.Cursor("3").Desc(10).Find(&results)
	require.NoError(t, err)
	assert.Len(t, results, 2)
	assert.Equal(t, "2", results[0].ID)
}

func TestCursorQuery_SearchLike_NoColumns(t *testing.T) {
	db := setupCursorDB(t,
		cursorTestItem{ID: "2", Name: "alpha"},
		cursorTestItem{ID: "1", Name: "beta"},
	)

	var results []cursorTestItem
	q := NewCursorQuery(db.Model(&cursorTestItem{})).SearchLike("alpha")
	err := q.Desc(10).Find(&results)
	require.NoError(t, err)
	assert.Len(t, results, 2, "no columns means no filter applied")
}
