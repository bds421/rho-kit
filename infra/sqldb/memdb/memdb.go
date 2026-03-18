// Package memdb provides a lightweight SQLite-backed *gorm.DB for unit tests.
// It uses goose migrations (not AutoMigrate) to ensure tests run against the
// same schema as production.
package memdb

import (
	"context"
	"io/fs"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/pressly/goose/v3"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// New returns a SQLite-backed *gorm.DB running in-memory. If migrations is
// non-nil, goose migrations are applied using the SQLite dialect.
//
// The migrations fs.FS should contain .sql files at its root (not in a
// subdirectory). Use [fs.Sub] if your embed.FS has a "migrations" prefix:
//
//	sub, _ := fs.Sub(myFS, "migrations")
//	db := memdb.New(t, sub)
func New(t testing.TB, migrations fs.FS) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Discard,
	})
	if err != nil {
		t.Fatalf("memdb: open sqlite: %v", err)
	}

	t.Cleanup(func() {
		sqlDB, err := db.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
	})

	if migrations != nil {
		sqlDB, err := db.DB()
		if err != nil {
			t.Fatalf("memdb: get sql.DB: %v", err)
		}
		provider, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, migrations)
		if err != nil {
			t.Fatalf("memdb: create goose provider: %v", err)
		}
		if _, err := provider.Up(context.Background()); err != nil {
			t.Fatalf("memdb: migrations failed: %v", err)
		}
	}

	return db
}
