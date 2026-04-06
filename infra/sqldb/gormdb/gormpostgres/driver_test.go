package gormpostgres

import (
	"testing"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
)

func TestPostgresDriver_Name(t *testing.T) {
	d := &PostgresDriver{}
	if got := d.Name(); got != "postgres" {
		t.Errorf("Name() = %q, want %q", got, "postgres")
	}
}

func TestPostgresDriver_ImplementsDriver(t *testing.T) {
	var _ gormdb.Driver = &PostgresDriver{}
}

func TestBuildPostgresDSN_Defaults(t *testing.T) {
	cfg := sqldb.Config{
		Host:     "localhost",
		Port:     5432,
		User:     "postgres",
		Password: "secret",
		Name:     "testdb",
	}
	got := buildPostgresDSN(cfg, false)
	want := "host='localhost' port=5432 user='postgres' password='secret' dbname='testdb' sslmode='disable'"
	if got != want {
		t.Errorf("buildPostgresDSN() =\n  %q\nwant\n  %q", got, want)
	}
}

func TestBuildPostgresDSN_TLSEnabled(t *testing.T) {
	cfg := sqldb.Config{
		Host:     "db.example.com",
		Port:     5432,
		User:     "app",
		Password: "pass",
		Name:     "mydb",
	}
	got := buildPostgresDSN(cfg, true)
	want := "host='db.example.com' port=5432 user='app' password='pass' dbname='mydb' sslmode='verify-full'"
	if got != want {
		t.Errorf("buildPostgresDSN() =\n  %q\nwant\n  %q", got, want)
	}
}

func TestBuildPostgresDSN_ExplicitSSLMode(t *testing.T) {
	cfg := sqldb.Config{
		Host:     "db.example.com",
		Port:     5432,
		User:     "app",
		Password: "pass",
		Name:     "mydb",
		Options:  map[string]string{"sslmode": "require"},
	}
	got := buildPostgresDSN(cfg, true)
	want := "host='db.example.com' port=5432 user='app' password='pass' dbname='mydb' sslmode='require'"
	if got != want {
		t.Errorf("buildPostgresDSN() =\n  %q\nwant\n  %q", got, want)
	}
}

func TestBuildPostgresDSN_EscapesSpecialChars(t *testing.T) {
	cfg := sqldb.Config{
		Host:     "db.example.com",
		Port:     5432,
		User:     "user'name",
		Password: "pass\\word",
		Name:     "test'db",
	}
	got := buildPostgresDSN(cfg, false)
	want := "host='db.example.com' port=5432 user='user\\'name' password='pass\\\\word' dbname='test\\'db' sslmode='disable'"
	if got != want {
		t.Errorf("buildPostgresDSN() =\n  %q\nwant\n  %q", got, want)
	}
}

func TestEscapePostgresValue(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"plain", "hello", "hello"},
		{"single_quote", "it's", "it\\'s"},
		{"backslash", `a\b`, `a\\b`},
		{"null_byte", "a\x00b", "ab"},
		{"newline", "a\nb", "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := escapePostgresValue(tt.input)
			if got != tt.want {
				t.Errorf("escapePostgresValue(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
