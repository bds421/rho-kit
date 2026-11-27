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
	eventsTable    = "audit_log_events"
	retentionTable = "audit_log_retention"

	// maxIdentifierBytes is PostgreSQL's NAMEDATALEN-1; a longer name is
	// silently truncated by the server, so it is refused here instead.
	maxIdentifierBytes = 63
)

// WithSchema makes the Store use the audit log tables in schema instead of
// the ones search_path resolves: every statement names them as
// "schema".audit_log_events and "schema".audit_log_retention. Apply the
// tables with [MigrationsInSchema] and the same schema. An empty or
// invalid name panics.
func WithSchema(schema string) Option {
	if err := validateSchema(schema); err != nil {
		panic("auditlog/postgres: " + err.Error())
	}
	events := pgx.Identifier{schema, eventsTable}.Sanitize()
	retention := pgx.Identifier{schema, retentionTable}.Sanitize()
	return func(s *Store) { s.events, s.retention = events, retention }
}

func validateSchema(schema string) error {
	// A backslash would be an escape in a string literal where
	// standard_conforming_strings is off.
	if schema == "" || len(schema) > maxIdentifierBytes || strings.ContainsAny(schema, "\x00\\") {
		return fmt.Errorf("schema must be 1 to %d bytes without NUL or backslash", maxIdentifierBytes)
	}
	return nil
}

// sql returns query, written against the unqualified tables, against the
// Store's tables.
func (s *Store) sql(query string) string {
	if s.events == "" {
		return query
	}
	// The retention table's name does not contain the events table's.
	query = strings.ReplaceAll(query, eventsTable, s.events)
	return strings.ReplaceAll(query, retentionTable, s.retention)
}

// MigrationsInSchema returns [Migrations] with schema put first on the
// search_path of every Up and Down section, so the tables and their
// indexes are created in schema and the later statements resolve there
// first; the rest of the search_path stays, so goose still finds its
// version table. A section that runs in goose's transaction sets it with
// is_local; one marked NO TRANSACTION sets it for the session and ends
// with RESET search_path, which goose runs on the same connection. The
// layout is that of [Migrations] (files under migrations/). The schema
// must exist (each migration fails otherwise) and the migrating login must
// be allowed to create in it. Give migrate.Config a schema-qualified
// TableName, so the version table does not depend on the session's
// search_path either.
func MigrationsInSchema(schema string) (fs.FS, error) {
	if err := validateSchema(schema); err != nil {
		return nil, fmt.Errorf("auditlog/postgres: %w", err)
	}
	files, err := fs.Glob(Migrations, "migrations/*.sql")
	if err != nil {
		return nil, fmt.Errorf("auditlog/postgres: list migrations: %w", err)
	}
	out := schemaFS{base: Migrations, files: make(map[string][]byte, len(files))}
	for _, name := range files {
		data, err := fs.ReadFile(Migrations, name)
		if err != nil {
			return nil, fmt.Errorf("auditlog/postgres: read %s: %w", name, err)
		}
		rewritten, err := withSearchPath(data, schema)
		if err != nil {
			return nil, fmt.Errorf("auditlog/postgres: %s: %w", name, err)
		}
		out.files[name] = rewritten
	}
	return out, nil
}

var (
	upMarker   = []byte("-- +goose Up\n")
	downMarker = []byte("-- +goose Down\n")
	noTxMarker = []byte("-- +goose NO TRANSACTION\n")
)

// searchPathFirst puts schema in front of the session's search_path, for
// the transaction (local) or the session, and then fails unless schema
// exists: PostgreSQL skips a missing schema on the search_path, so the
// tables would otherwise land in the next one, typically public.
func searchPathFirst(schema string, local bool) []byte {
	literal := strings.ReplaceAll(pgx.Identifier{schema}.Sanitize(), "'", "''")
	return fmt.Appendf(nil, "SELECT set_config('search_path', concat_ws(', ', '%s', nullif(current_setting('search_path'), '')), %t);\n"+
		"SELECT '%s'::regnamespace;\n", literal, local, literal)
}

var resetSearchPath = []byte("\nRESET search_path;\n")

func withSearchPath(data []byte, schema string) ([]byte, error) {
	if bytes.Count(data, upMarker) != 1 || bytes.Count(data, downMarker) > 1 {
		return nil, errors.New("needs one Up and at most one Down section")
	}
	noTx := bytes.Contains(data, noTxMarker)
	set := searchPathFirst(schema, !noTx)
	up, down, hasDown := bytes.Cut(data, downMarker)
	before, upBody, _ := bytes.Cut(up, upMarker)
	var out bytes.Buffer
	section := func(marker, body []byte) {
		out.Write(marker)
		out.Write(set)
		out.Write(body)
		if noTx {
			out.Write(resetSearchPath)
		}
	}
	out.Write(before)
	section(upMarker, upBody)
	if hasDown {
		section(downMarker, down)
	}
	return out.Bytes(), nil
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
