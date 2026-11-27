package postgres

import (
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/observability/v2/auditlog"
)

func TestWithSchemaQualifiesEveryStatement(t *testing.T) {
	t.Parallel()
	s := &Store{}
	WithSchema(`kit "a"`)(s)
	q, _ := querySQL(auditlog.Filter{Search: "x"}, "c", time.Now(), "id", 10)
	for _, query := range []string{
		s.sql(q),
		s.sql(`INSERT INTO audit_log_retention (id) VALUES (1)`),
		s.sql(`DELETE FROM audit_log_events WHERE seq <= $1`),
	} {
		unqualified := strings.NewReplacer(`"kit ""a"""."audit_log_events"`, "", `"kit ""a"""."audit_log_retention"`, "").Replace(query)
		assert.NotContains(t, unqualified, "audit_log_", query)
	}
	assert.Contains(t, s.sql(q), `FROM "kit ""a"""."audit_log_events"`)
}

func TestWithoutSchemaKeepsStatements(t *testing.T) {
	t.Parallel()
	const q = `SELECT count(*) FROM audit_log_events`
	assert.Equal(t, q, (&Store{}).sql(q))
}

func TestWithSchemaRejectsInvalidNames(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "a\x00b", strings.Repeat("s", 64), `a\b`} {
		assert.Panics(t, func() { WithSchema(name) }, "%q", name)
		_, err := MigrationsInSchema(name)
		assert.Error(t, err, "%q", name)
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

	set := func(local bool) string {
		flag := "false"
		if local {
			flag = "true"
		}
		return `SELECT set_config('search_path', concat_ws(', ', '"it''s"', nullif(current_setting('search_path'), '')), ` + flag + `);` + "\n" +
			`SELECT '"it''s"'::regnamespace;` + "\n"
	}
	var noTx int
	for _, name := range names {
		data, err := fs.ReadFile(dir, name)
		require.NoError(t, err)
		text := string(data)
		session := strings.Contains(text, "-- +goose NO TRANSACTION\n")
		up, down, hasDown := strings.Cut(text, "-- +goose Down\n")
		assert.Contains(t, up, "-- +goose Up\n"+set(!session), name)
		if hasDown {
			assert.True(t, strings.HasPrefix(down, set(!session)), name)
		}
		if session {
			noTx++
			assert.True(t, strings.HasSuffix(up, "RESET search_path;\n"), "%s: the Up section resets the session's search_path", name)
			if hasDown {
				assert.True(t, strings.HasSuffix(down, "RESET search_path;\n"), name)
			}
		}
		info, err := fs.Stat(dir, name)
		require.NoError(t, err)
		assert.Equal(t, int64(len(data)), info.Size(), name)
	}
	assert.Positive(t, noTx, "the set has migrations outside a transaction")
}

func TestWithSearchPathRefusesUnsupportedFiles(t *testing.T) {
	t.Parallel()
	_, err := withSearchPath([]byte("SELECT 1;\n"), "s")
	require.Error(t, err)
	_, err = withSearchPath([]byte("-- +goose Up\nSELECT 1;\n-- +goose Down\n-- +goose Down\n"), "s")
	require.Error(t, err)
}
