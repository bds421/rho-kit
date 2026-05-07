// Package memdb provides a lightweight SQLite-backed *gorm.DB for unit tests.
// It uses goose migrations (not AutoMigrate) to ensure tests run against the
// same schema as production.
//
// # Postgres-portability rewrite
//
// Some Postgres-specific column types (notably TIMESTAMPTZ) are not
// recognised by the underlying SQLite driver and trip the
// time.Time-scan path. memdb rewrites the migration SQL on read so
// production-shape Postgres migrations run unmodified against the
// in-memory SQLite test backend. Callers can disable the rewrite via
// [WithoutSQLRewrite] when their migrations are SQLite-native and they
// don't want surprises from the substitution.
package memdb

import (
	"context"
	"io"
	"io/fs"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/pressly/goose/v3"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Option configures memdb construction.
type Option func(*config)

type config struct {
	rewriteSQL bool
}

// WithoutSQLRewrite disables the Postgres-to-SQLite SQL substitutions.
// Default is enabled — see the package doc for what gets rewritten.
func WithoutSQLRewrite() Option {
	return func(c *config) { c.rewriteSQL = false }
}

// New returns a SQLite-backed *gorm.DB running in-memory. If migrations is
// non-nil, goose migrations are applied using the SQLite dialect.
//
// The migrations fs.FS should contain .sql files at its root (not in a
// subdirectory). Use [fs.Sub] if your embed.FS has a "migrations" prefix:
//
//	sub, _ := fs.Sub(myFS, "migrations")
//	db := memdb.New(t, sub)
func New(t testing.TB, migrations fs.FS, opts ...Option) *gorm.DB {
	t.Helper()

	cfg := config{rewriteSQL: true}
	for _, o := range opts {
		o(&cfg)
	}

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
		applied := migrations
		if cfg.rewriteSQL {
			applied = rewriteFS{fs: migrations}
		}
		provider, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, applied)
		if err != nil {
			t.Fatalf("memdb: create goose provider: %v", err)
		}
		if _, err := provider.Up(context.Background()); err != nil {
			t.Fatalf("memdb: migrations failed: %v", err)
		}
	}

	return db
}

// rewriteFS wraps an fs.FS, transforming SQL files on read so that
// Postgres-specific syntax compiles under SQLite. Non-SQL files are
// passed through untouched.
type rewriteFS struct {
	fs fs.FS
}

func (r rewriteFS) Open(name string) (fs.File, error) {
	f, err := r.fs.Open(name)
	if err != nil {
		return nil, err
	}
	if path.Ext(name) != ".sql" {
		return f, nil
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	return &memFile{
		name: name,
		data: rewriteForSQLite(data),
	}, nil
}

// rewriteForSQLite applies the Postgres → SQLite type substitutions.
// The list is intentionally narrow: each substitution is here because a
// real production migration in this repo trips the SQLite test driver
// without it.
func rewriteForSQLite(in []byte) []byte {
	s := string(in)
	// TIMESTAMPTZ is the canonical Postgres alias for TIMESTAMP WITH
	// TIME ZONE. SQLite's sqlite3 driver doesn't recognise it as a
	// time-affinity column, so the *time.Time scan fails. SQLite has no
	// native time type — TEXT-affinity TIMESTAMP triggers the driver's
	// ISO-8601 parsing path. The Go side already enforces UTC, so the
	// substitution is semantically equivalent for the test.
	s = strings.ReplaceAll(s, "TIMESTAMPTZ", "TIMESTAMP")
	s = strings.ReplaceAll(s, "TIMESTAMP WITH TIME ZONE", "TIMESTAMP")
	return []byte(s)
}

type memFile struct {
	name string
	data []byte
	off  int
}

func (m *memFile) Stat() (fs.FileInfo, error) {
	return zeroFileInfo{name: m.name, size: int64(len(m.data))}, nil
}
func (m *memFile) Read(p []byte) (int, error) {
	if m.off >= len(m.data) {
		return 0, io.EOF
	}
	n := copy(p, m.data[m.off:])
	m.off += n
	return n, nil
}
func (m *memFile) Close() error { return nil }

type zeroFileInfo struct {
	name string
	size int64
}

func (z zeroFileInfo) Name() string      { return path.Base(z.name) }
func (z zeroFileInfo) Size() int64       { return z.size }
func (z zeroFileInfo) Mode() fs.FileMode { return 0o444 }
func (z zeroFileInfo) ModTime() time.Time { return time.Time{} }
func (z zeroFileInfo) IsDir() bool       { return false }
func (z zeroFileInfo) Sys() any          { return nil }
