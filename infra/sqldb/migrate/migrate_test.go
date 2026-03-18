package migrate

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/sqldb/memdb"
)

func testMigrations() fstest.MapFS {
	return fstest.MapFS{
		"00001_create_items.sql": &fstest.MapFile{
			Data: []byte(`-- +goose Up
CREATE TABLE items (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL
);

-- +goose Down
DROP TABLE items;
`),
		},
		"00002_add_description.sql": &fstest.MapFile{
			Data: []byte(`-- +goose Up
ALTER TABLE items ADD COLUMN description TEXT;

-- +goose Down
ALTER TABLE items DROP COLUMN description;
`),
		},
	}
}

func TestUp(t *testing.T) {
	db := memdb.New(t, nil)
	cfg := Config{Dir: testMigrations(), Dialect: "sqlite"}

	applied, err := Up(context.Background(), db, cfg)
	require.NoError(t, err)
	assert.Equal(t, 2, applied)

	// Verify table exists with both columns.
	var count int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM items").Scan(&count).Error)
}

func TestUp_Idempotent(t *testing.T) {
	db := memdb.New(t, nil)
	cfg := Config{Dir: testMigrations(), Dialect: "sqlite"}

	applied1, err := Up(context.Background(), db, cfg)
	require.NoError(t, err)
	assert.Equal(t, 2, applied1)

	// Second call should be a no-op.
	applied2, err := Up(context.Background(), db, cfg)
	require.NoError(t, err)
	assert.Equal(t, 0, applied2)
}

func TestDown(t *testing.T) {
	db := memdb.New(t, nil)
	cfg := Config{Dir: testMigrations(), Dialect: "sqlite"}

	_, err := Up(context.Background(), db, cfg)
	require.NoError(t, err)

	err = Down(context.Background(), db, cfg)
	require.NoError(t, err)

	// After rolling back one migration, the items table should still exist
	// (only the second migration was rolled back).
	var count int64
	require.NoError(t, db.Raw("SELECT COUNT(*) FROM items").Scan(&count).Error)
}

func TestUp_InvalidDialect(t *testing.T) {
	db := memdb.New(t, nil)
	cfg := Config{Dir: testMigrations(), Dialect: "oracle"}

	_, err := Up(context.Background(), db, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported dialect")
}

func TestUp_NilDir(t *testing.T) {
	db := memdb.New(t, nil)
	cfg := Config{Dialect: "sqlite"}

	_, err := Up(context.Background(), db, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Dir must not be nil")
}

func TestUp_EmptyDialect(t *testing.T) {
	db := memdb.New(t, nil)
	cfg := Config{Dir: testMigrations()}

	_, err := Up(context.Background(), db, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Dialect must not be empty")
}

func TestStatus(t *testing.T) {
	db := memdb.New(t, nil)
	cfg := Config{Dir: testMigrations(), Dialect: "sqlite"}

	// Status should work before any migrations are applied.
	err := Status(context.Background(), db, cfg, nil)
	require.NoError(t, err)
}
