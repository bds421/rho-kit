package memdb

import (
	"embed"
	"io/fs"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/*.sql
var testMigrationsRaw embed.FS

func testMigrations(t *testing.T) fs.FS {
	t.Helper()
	sub, err := fs.Sub(testMigrationsRaw, "testdata")
	require.NoError(t, err)
	return sub
}

func TestNew_WithMigrations(t *testing.T) {
	db := New(t, testMigrations(t))

	assert.True(t, db.Migrator().HasTable("test_users"))

	result := db.Exec("INSERT INTO test_users (name, age) VALUES (?, ?)", "Alice", 30)
	require.NoError(t, result.Error)

	var name string
	db.Raw("SELECT name FROM test_users WHERE age = ?", 30).Scan(&name)
	assert.Equal(t, "Alice", name)
}

func TestNew_NilMigrations(t *testing.T) {
	db := New(t, nil)
	assert.NotNil(t, db)
}

func TestNew_Isolation(t *testing.T) {
	db1 := New(t, testMigrations(t))
	db2 := New(t, testMigrations(t))

	db1.Exec("INSERT INTO test_users (name, age) VALUES (?, ?)", "Alice", 30)

	var count int64
	db2.Raw("SELECT COUNT(*) FROM test_users").Scan(&count)
	assert.Equal(t, int64(0), count)
}
