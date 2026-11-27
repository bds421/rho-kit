package postgres

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	outboxTable = "outbox_entries"
	inboxTable  = "inbox_entries"

	// maxIdentifierBytes is PostgreSQL's NAMEDATALEN-1; a longer name is
	// silently truncated by the server, so it is refused here instead.
	maxIdentifierBytes = 63
)

// WithSchema makes the Store use the outbox_entries table in schema instead
// of the one search_path resolves. Every statement names the table as
// "schema".outbox_entries; apply the tables with [MigrationsInSchema] and
// the same schema. An empty or invalid name panics.
func WithSchema(schema string) Option {
	table := qualified(schema, outboxTable)
	return func(s *Store) { s.table = table }
}

// WithInboxSchema makes the Inbox use the inbox_entries table in schema,
// as [WithSchema] does for the Store.
func WithInboxSchema(schema string) InboxOption {
	table := qualified(schema, inboxTable)
	return func(s *Inbox) { s.table = table }
}

func qualified(schema, table string) string {
	if err := validateSchema(schema); err != nil {
		panic("outbox/postgres: " + err.Error())
	}
	return pgx.Identifier{schema, table}.Sanitize()
}

func validateSchema(schema string) error {
	// A backslash would be an escape in a string literal where
	// standard_conforming_strings is off.
	if schema == "" || len(schema) > maxIdentifierBytes || strings.ContainsAny(schema, "\x00\\") {
		return fmt.Errorf("schema must be 1 to %d bytes without NUL or backslash", maxIdentifierBytes)
	}
	return nil
}

// inTable names table in query, which is written against the unqualified
// defaultTable; without a schema option the query stays as written.
func inTable(query, defaultTable, table string) string {
	if table == "" || table == defaultTable {
		return query
	}
	return strings.ReplaceAll(query, defaultTable, table)
}

// sql returns query against the Store's table.
func (s *Store) sql(query string) string { return inTable(query, outboxTable, s.table) }

// sql returns query against the Inbox's table.
func (s *Inbox) sql(query string) string { return inTable(query, inboxTable, s.table) }

// MigrationsInSchema returns [Migrations] with schema put first on the
// search_path of every Up and Down section (set_config with is_local, so
// it ends with the transaction goose opens per file). The tables and their
// indexes are created in schema and the later ALTERs resolve there first;
// the rest of the search_path stays, so goose still finds its version
// table. The layout is that of [Migrations] (files under migrations/). The
// schema must exist (each migration fails otherwise) and the migrating
// login must be allowed to create in it. Give migrate.Config a
// schema-qualified TableName (for example "outbox.goose_db_version"), so
// the version table does not depend on the session's search_path either.
//
// A migration marked NO TRANSACTION, or one without exactly one Up and at
// most one Down marker, is refused: the setting would outlive it.
func MigrationsInSchema(schema string) (fs.FS, error) {
	if err := validateSchema(schema); err != nil {
		return nil, fmt.Errorf("outbox/postgres: %w", err)
	}
	files, err := fs.Glob(Migrations, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("outbox/postgres: list migrations: %w", err)
	}
	set := searchPathFirst(schema)
	out := schemaFS{base: Migrations, files: make(map[string][]byte, len(files))}
	for _, name := range files {
		data, err := fs.ReadFile(Migrations, name)
		if err != nil {
			return nil, fmt.Errorf("outbox/postgres: read %s: %w", name, err)
		}
		rewritten, err := withSearchPath(data, set)
		if err != nil {
			return nil, fmt.Errorf("outbox/postgres: %s: %w", name, err)
		}
		out.files[name] = rewritten
	}
	return out, nil
}

// searchPathFirst are the statements that put schema in front of the
// session's search_path for the current transaction and then fail unless
// schema exists: PostgreSQL skips a missing schema on the search_path, so
// the tables would otherwise land in the next one, typically public.
func searchPathFirst(schema string) []byte {
	literal := strings.ReplaceAll(pgx.Identifier{schema}.Sanitize(), "'", "''")
	return []byte("SELECT set_config('search_path', concat_ws(', ', '" + literal +
		"', nullif(current_setting('search_path'), '')), true);\n" +
		"SELECT '" + literal + "'::regnamespace;\n")
}

var (
	upMarker   = []byte("-- +goose Up\n")
	downMarker = []byte("-- +goose Down\n")
)

func withSearchPath(data, set []byte) ([]byte, error) {
	if bytes.Contains(data, []byte("NO TRANSACTION")) {
		return nil, errors.New("a NO TRANSACTION migration would keep the search_path")
	}
	if bytes.Count(data, upMarker) != 1 || bytes.Count(data, downMarker) > 1 {
		return nil, errors.New("needs one Up and at most one Down section")
	}
	data = bytes.Replace(data, upMarker, append(append([]byte{}, upMarker...), set...), 1)
	return bytes.Replace(data, downMarker, append(append([]byte{}, downMarker...), set...), 1), nil
}

// schemaFS serves the rewritten migration files and everything else
// (the directory listings) from base.
type schemaFS struct {
	base  fs.FS
	files map[string][]byte
}

func (f schemaFS) Open(name string) (fs.File, error) {
	data, ok := f.files[name]
	if !ok {
		return f.base.Open(name)
	}
	return &memFile{Reader: bytes.NewReader(data), name: path.Base(name), size: int64(len(data))}, nil
}

// memFile is one rewritten migration file.
type memFile struct {
	*bytes.Reader
	name string
	size int64
}

func (m *memFile) Stat() (fs.FileInfo, error) { return m, nil }
func (m *memFile) Close() error               { return nil }

func (m *memFile) Name() string       { return m.name }
func (m *memFile) Size() int64        { return m.size }
func (m *memFile) Mode() fs.FileMode  { return 0o444 }
func (m *memFile) ModTime() time.Time { return time.Time{} }
func (m *memFile) IsDir() bool        { return false }
func (m *memFile) Sys() any           { return nil }
