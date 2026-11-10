package expandonly

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func migration(up string) string { return "-- +goose Up\n" + up + "\n-- +goose Down\nDROP TABLE t;\n" }

func check(t *testing.T, files map[string]string, after int64) []Finding {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, src := range files {
		fsys[name] = &fstest.MapFile{Data: []byte(src)}
	}
	findings, err := Check(Options{Sets: []Set{{Name: "orders", Dir: fsys, After: after}}})
	if err != nil {
		t.Fatal(err)
	}
	return findings
}

// What the previous release keeps working with passes: new objects, a
// nullable column or one with a default, a plain index, backfills and
// deletes, grants, and the Down section, which never runs on a rollback.
func TestExpandingMigrationsPass(t *testing.T) {
	findings := check(t, map[string]string{"00002_expand.sql": migration(`
CREATE TABLE notes (id bigint PRIMARY KEY, body text NOT NULL);
ALTER TABLE notes ADD CONSTRAINT body_set CHECK (body <> '');
CREATE UNIQUE INDEX notes_body ON notes (body);
ALTER TABLE orders ADD COLUMN note text;
ALTER TABLE orders ADD COLUMN state text NOT NULL DEFAULT 'open';
ALTER TABLE orders ADD COLUMN seq bigint GENERATED ALWAYS AS IDENTITY;
ALTER TABLE orders ADD COLUMN qty int CHECK (qty >= 0) NOT NULL DEFAULT 0;
ALTER TABLE orders ADD COLUMN owner_id bigint REFERENCES notes (id);
ALTER TABLE orders ADD COLUMN n int CHECK (n > 0);
ALTER TABLE orders ALTER COLUMN ref DROP NOT NULL;
ALTER TABLE orders ALTER COLUMN note SET DEFAULT '';
CREATE INDEX orders_note ON orders (note);
CREATE INDEX CONCURRENTLY orders_state ON orders (state);
UPDATE orders SET state = 'open' WHERE state IS NULL;
DELETE FROM orders WHERE created < now() - interval '1 year';
GRANT SELECT ON notes TO reader;
CREATE FUNCTION g() RETURNS int LANGUAGE sql AS 'SELECT 1';
REVOKE ALL ON FUNCTION g() FROM PUBLIC;
REVOKE ALL ON notes FROM PUBLIC;
CREATE INDEX orders_tmp ON orders (note);
DROP INDEX orders_tmp;
CREATE OR REPLACE FUNCTION f() RETURNS int LANGUAGE sql AS $$ DROP TABLE x; SELECT 1 $$;
ALTER TYPE state ADD VALUE 'held';
DO $$ BEGIN IF NOT EXISTS (SELECT 1) THEN ALTER TABLE orders ADD COLUMN extra text; END IF; END $$;
CREATE TABLE archive.notes (id int);
ALTER TABLE archive.notes RENAME TO notes2;
CREATE TABLE logs (id int, at timestamptz);
ALTER TABLE logs RENAME COLUMN at TO logged;
ALTER TABLE public.logs OWNER TO app;
CREATE POLICY lp ON logs USING (true);
ALTER POLICY lp ON logs USING (false);
TRUNCATE logs;
REVOKE ALL ON public.logs FROM PUBLIC;
DO $$ BEGIN ALTER TABLE logs DROP COLUMN logged; END $$;
ALTER TABLE logs SET SCHEMA archive;
DROP FUNCTION g();
CREATE SCHEMA staging;
REVOKE ALL ON SCHEMA staging FROM PUBLIC;
ALTER TABLE orders ADD COLUMN code text UNIQUE;
DROP TABLE notes;`)}, 1)
	for _, f := range findings {
		t.Error(f)
	}
}

// Each contracting statement is found, at its line, with how to declare it.
func TestContractingStatementsAreFound(t *testing.T) {
	for stmt, reason := range map[string]string{
		"DROP TABLE orders;":                                          "drops a table",
		"DROP INDEX orders_ref;":                                      "drops an index",
		"DROP VIEW open_orders;":                                      "drops a view",
		"DROP FUNCTION f();":                                          "drops a function",
		"ALTER TABLE orders DROP COLUMN ref;":                         "drops column ref",
		"ALTER TABLE orders DROP CONSTRAINT orders_ref_key;":          "drops constraint orders_ref_key",
		"ALTER TABLE orders RENAME COLUMN ref TO reference;":          "renames",
		"ALTER TABLE orders RENAME TO purchases;":                     "renames",
		"ALTER TABLE orders ALTER COLUMN ref TYPE bigint;":            "changes the type of column ref",
		"ALTER TABLE orders ALTER COLUMN ref SET NOT NULL;":           "makes column ref NOT NULL",
		"ALTER TABLE orders ALTER COLUMN ref DROP DEFAULT;":           "drops the default of column ref",
		"ALTER TABLE orders ADD CONSTRAINT c CHECK (ref > 0);":        "adds a constraint",
		"ALTER TABLE orders ADD COLUMN owner text NOT NULL;":          "NOT NULL without a default",
		"ALTER TABLE orders ENABLE ROW LEVEL SECURITY;":               "row-level security",
		"ALTER TABLE orders SET SCHEMA archive;":                      "moves an object",
		"CREATE UNIQUE INDEX orders_ref ON orders (ref);":             "unique index",
		"REVOKE SELECT ON orders FROM reader;":                        "revokes a privilege",
		"TRUNCATE orders;":                                            "truncates",
		"CREATE POLICY p ON orders AS RESTRICTIVE USING (true);":      "restrictive policy",
		"ALTER TYPE state RENAME VALUE 'open' TO 'opened';":           "renames",
		"DO $$ BEGIN ALTER TABLE orders DROP COLUMN ref; END $$;":     "drops column ref",
		"DO $$ BEGIN EXECUTE 'DROP TABLE orders'; END $$;":            "dynamic SQL",
		"REVOKE reader FROM app;":                                     "revokes a role",
		"ALTER ROLE app SET search_path = archive;":                   "a setting",
		"ALTER DATABASE app SET search_path = archive;":               "a setting",
		"ALTER TABLE orders OWNER TO app;":                            "changes the owner",
		"ALTER FUNCTION f() OWNER TO app;":                            "changes the owner",
		"DROP DATABASE app;":                                          "drops a database",
		"DROP TABLESPACE fast;":                                       "drops a tablespace",
		"ALTER TABLE orders ADD COLUMN code text DEFAULT 'x' UNIQUE;": "unique with a default",
		"ALTER TABLE orders ALTER COLUMN ref SET DEFAULT NULL;":       "drops the default",
		"ALTER TABLE archive.notes DROP COLUMN id;":                   "drops column id",
		"DROP TABLE public.notes, orders;":                            "drops a table",
	} {
		findings := check(t, map[string]string{"00002_change.sql": migration("CREATE TABLE notes (id int);\n-- why\n" + stmt)}, 1)
		if len(findings) != 1 {
			t.Errorf("%s: %d findings %v", stmt, len(findings), findings)
			continue
		}
		f := findings[0]
		if !strings.Contains(f.Reason, reason) || f.Line != 4 || f.File != "orders/00002_change.sql" {
			t.Errorf("%s: %+v", stmt, f)
		}
		if s := f.String(); !strings.HasPrefix(s, "orders/00002_change.sql:4: `") || !strings.Contains(s, "-- contract: vX.Y.Z <why>") {
			t.Errorf("%s: message %q", stmt, s)
		}
	}
}

// A declared contraction passes, history is not checked, and a malformed
// declaration is a finding.
func TestDeclarationsAndHistory(t *testing.T) {
	findings := check(t, map[string]string{
		"00001_old.sql":      migration("ALTER TABLE orders DROP COLUMN legacy;"),
		"00002_declared.sql": "-- contract: v1.4.0 ref is unused since v1.4.0\n" + migration("ALTER TABLE orders DROP COLUMN ref;"),
		"00003_bad.sql":      "-- contract: soon\n" + migration("ALTER TABLE orders DROP COLUMN note;"),
		"00004_bare.sql":     "-- contract: v1.4.0\n" + migration("ALTER TABLE orders DROP COLUMN qty;"),
	}, 1)
	if len(findings) != 4 {
		t.Fatalf("findings %v", findings)
	}
	for _, f := range findings {
		if f.File != "orders/00003_bad.sql" && f.File != "orders/00004_bare.sql" {
			t.Errorf("unexpected %v", f)
		}
	}
}

// Only what the file itself created is exempt: by schema and kind, and
// not when IF NOT EXISTS or OR REPLACE may have met an existing object.
func TestOnlyObjectsTheFileCreatedAreExempt(t *testing.T) {
	for _, up := range []string{
		"CREATE TABLE archive.orders (id int);\nALTER TABLE orders DROP COLUMN ref;",
		"CREATE FUNCTION orders() RETURNS int LANGUAGE sql AS 'SELECT 1';\nDROP TABLE orders;",
		"CREATE SCHEMA orders;\nREVOKE ALL ON orders FROM PUBLIC;",
		"CREATE TABLE IF NOT EXISTS orders (id int);\nALTER TABLE orders DROP COLUMN ref;",
		"CREATE OR REPLACE VIEW open_orders AS SELECT 1;\nDROP VIEW open_orders;",
		"CREATE INDEX IF NOT EXISTS orders_ref ON orders (ref);\nDROP INDEX orders_ref;",
	} {
		if findings := check(t, map[string]string{"00002_x.sql": migration(up)}, 1); len(findings) != 1 {
			t.Errorf("%q: %v", up, findings)
		}
	}
}

// A finding names the statement's own line, past block comments and goose
// annotations, and quotes it without them.
func TestFindingsNameTheStatementLine(t *testing.T) {
	up := "/* why:\n   it goes */\n-- +goose StatementBegin\nDROP TABLE orders;\n-- +goose StatementEnd"
	findings := check(t, map[string]string{"00002_x.sql": migration(up)}, 1)
	if len(findings) != 1 || findings[0].Line != 5 || findings[0].Statement != "`DROP TABLE orders`" {
		t.Errorf("%+v", findings)
	}
}

// The floor file holds the newest release a contract comment names.
func TestTheFloorFollowsTheNewestDeclaration(t *testing.T) {
	fsys := fstest.MapFS{
		"00002_a.sql": {Data: []byte("-- contract: v1.10.0 a\n" + migration("DROP TABLE a;"))},
		"00003_b.sql": {Data: []byte("-- contract: v1.9.2 b\n" + migration("DROP TABLE b;"))},
	}
	floor := filepath.Join(t.TempDir(), "rollback-floor")
	for content, ok := range map[string]bool{"v1.10.0\n": true, "v1.9.2\n": false, "": false} {
		if err := os.WriteFile(floor, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		findings, err := Check(Options{Sets: []Set{{Name: "s", Dir: fsys}}, Floor: floor})
		if err != nil {
			t.Fatal(err)
		}
		if (len(findings) == 0) != ok {
			t.Errorf("floor %q: %v", content, findings)
		}
	}
	empty := fstest.MapFS{"00002_a.sql": {Data: []byte(migration("CREATE TABLE a (id int);"))}}
	missing := filepath.Join(t.TempDir(), "none")
	if findings, err := Check(Options{Sets: []Set{{Name: "s", Dir: empty}}, Floor: missing}); err != nil || len(findings) != 0 {
		t.Errorf("no declaration, no floor file: %v %v", findings, err)
	}
}

// A file that does not parse is an error, not a pass.
func TestAnUnparsableMigrationFails(t *testing.T) {
	fsys := fstest.MapFS{"00002_x.sql": {Data: []byte(migration("ALTER TABLE orders DROPP COLUMN ref;"))}}
	if _, err := Check(Options{Sets: []Set{{Name: "s", Dir: fsys}}}); err == nil {
		t.Error("an unparsable migration passed")
	}
}
