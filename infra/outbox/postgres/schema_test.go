package postgres

import (
	"context"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithSchemaQualifiesEveryStatement(t *testing.T) {
	t.Parallel()
	s := &Store{}
	WithSchema(`tenant "a"`)(s)
	WithClaimRoutes(ClaimRoute{Topic: "events", RoutingKey: "user.created", MessageType: "UserCreated"})(s)

	predicate, _ := s.routeSelection([]any{1})
	q := s.sql(fetchPendingQuery(predicate))
	assert.NotContains(t, strings.ReplaceAll(q, `"tenant ""a"""."outbox_entries"`, ""), "outbox_entries",
		"every table reference must be qualified")
	assert.Contains(t, q, `FROM "tenant ""a"""."outbox_entries"`)
	assert.Contains(t, q, `WHERE route.topic = "tenant ""a"""."outbox_entries".topic`)
}

func TestWithoutSchemaKeepsStatements(t *testing.T) {
	t.Parallel()
	s := &Store{}
	const q = `SELECT count(*) FROM outbox_entries`
	assert.Equal(t, q, s.sql(q))
}

func TestWithSchemaRejectsInvalidNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "a\x00b", strings.Repeat("s", 64), `a\b`} {
		assert.Panics(t, func() { WithSchema(name) }, "%q", name)
		assert.Panics(t, func() { WithInboxSchema(name) }, "%q", name)
		_, err := MigrationsInSchema(name)
		assert.Error(t, err, "%q", name)
	}
}

// schemaPool records the statements an Inbox sends.
type schemaPool struct{ sql []string }

func (p *schemaPool) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) { return nil, nil }
func (p *schemaPool) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	p.sql = append(p.sql, sql)
	return pgconn.NewCommandTag("DELETE 0"), nil
}
func (p *schemaPool) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	p.sql = append(p.sql, sql)
	return fakeRow{}
}

func TestWithInboxSchemaQualifiesStatements(t *testing.T) {
	t.Parallel()
	pool := &schemaPool{}
	inbox := newInboxWithPool(pool)
	WithInboxSchema("outbox")(inbox)

	_, err := inbox.PruneBefore(t.Context(), time.Now())
	require.NoError(t, err)
	_, err = inbox.Count(t.Context())
	require.NoError(t, err)
	require.Len(t, pool.sql, 2)
	for _, sql := range pool.sql {
		assert.Contains(t, sql, `"outbox"."inbox_entries"`)
	}
}

func TestMigrationsInSchemaSetsSearchPathPerSection(t *testing.T) {
	t.Parallel()
	dir, err := MigrationsInSchema("it's")
	require.NoError(t, err)

	names, err := fs.Glob(dir, "migrations/*.sql")
	require.NoError(t, err)
	original, err := fs.Glob(Migrations, "migrations/*.sql")
	require.NoError(t, err)
	require.Equal(t, original, names, "the layout must stay that of Migrations")

	set := `SELECT set_config('search_path', concat_ws(', ', '"it''s"', nullif(current_setting('search_path'), '')), true);` + "\n" +
		`SELECT '"it''s"'::regnamespace;` + "\n"
	sub, err := fs.Sub(dir, "migrations")
	require.NoError(t, err)
	for _, name := range names {
		data, err := fs.ReadFile(sub, strings.TrimPrefix(name, "migrations/"))
		require.NoError(t, err)
		text := string(data)
		assert.Contains(t, text, "-- +goose Up\n"+set, name)
		if strings.Contains(text, "-- +goose Down") {
			assert.Contains(t, text, "-- +goose Down\n"+set, name)
		}
		info, err := fs.Stat(sub, strings.TrimPrefix(name, "migrations/"))
		require.NoError(t, err)
		assert.Equal(t, int64(len(data)), info.Size(), name)
	}
}

func TestWithSearchPathRefusesUnsupportedFiles(t *testing.T) {
	t.Parallel()
	set := searchPathFirst("s")
	_, err := withSearchPath([]byte("-- +goose NO TRANSACTION\n-- +goose Up\nSELECT 1;\n"), set)
	require.Error(t, err)
	_, err = withSearchPath([]byte("SELECT 1;\n"), set)
	require.Error(t, err)
}
