package gormpostgres

import (
	"crypto/tls"
	"log/slog"
	"strings"
	"testing"

	"github.com/bds421/rho-kit/infra/sqldb"
	"github.com/bds421/rho-kit/infra/sqldb/gormdb"
)

func TestPostgresDriver_Name(t *testing.T) {
	d := PostgresDriver{}
	if got := d.Name(); got != "postgres" {
		t.Errorf("Name() = %q, want %q", got, "postgres")
	}
}

func TestPostgresDriver_ImplementsDriver(t *testing.T) {
	var _ gormdb.Driver = PostgresDriver{}
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
	// Default sslmode is "prefer" — strictly safer than the previous
	// "disable" default. Production callers should set DB_SSL_MODE
	// explicitly (Validate enforces this in non-dev environments).
	want := "host='localhost' port=5432 user='postgres' password='secret' dbname='testdb' sslmode='prefer'"
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
	want := "host='db.example.com' port=5432 user='user\\'name' password='pass\\\\word' dbname='test\\'db' sslmode='prefer'"
	if got != want {
		t.Errorf("buildPostgresDSN() =\n  %q\nwant\n  %q", got, want)
	}
}

// TestPostgresDriver_Open_RequiresExplicitSSLMode pins the LOW finding:
// direct driver construction must not silently default to "prefer" because
// the standalone caller bypasses sqldb.Fields.Validate. Without an explicit
// sslmode the driver could negotiate plaintext against a server that
// declines TLS.
func TestPostgresDriver_Open_RequiresExplicitSSLMode(t *testing.T) {
	cfg := sqldb.Config{
		Host: "127.0.0.1", Port: 1,
		User: "u", Password: "p", Name: "db",
	}
	_, err := PostgresDriver{}.Open(cfg, sqldb.DefaultPool(), slog.Default(), nil)
	if err == nil {
		t.Fatal("expected error for missing sslmode")
	}
	if !strings.Contains(err.Error(), "sslmode is required") {
		t.Errorf("error %q should mention required sslmode", err.Error())
	}
}

// TestPostgresDriver_Open_RejectsInsecureSkipVerifyUnderVerifyFull pins the
// MEDIUM finding: passing a *tls.Config with InsecureSkipVerify=true while
// sslmode is verify-full would defeat the strict TLS guarantee. Reject at
// driver entry before any connection is attempted.
func TestPostgresDriver_Open_RejectsInsecureSkipVerifyUnderVerifyFull(t *testing.T) {
	cfg := sqldb.Config{
		Host: "127.0.0.1", Port: 1,
		User: "u", Password: "p", Name: "db",
		Options: map[string]string{"sslmode": "verify-full"},
	}
	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	_, err := PostgresDriver{}.Open(cfg, sqldb.DefaultPool(), slog.Default(), tlsCfg)
	if err == nil {
		t.Fatal("expected error for InsecureSkipVerify under verify-full")
	}
	if !strings.Contains(err.Error(), "InsecureSkipVerify") {
		t.Errorf("error %q should mention InsecureSkipVerify", err.Error())
	}
}

// TestPostgresDriver_Open_RejectsInsecureSkipVerifyUnderEscalation: when
// sslmode=disable or =prefer is used together with a clientTLS bundle, the
// driver escalates to verify-full. The same InsecureSkipVerify rejection
// must apply because the user opted into TLS by supplying a bundle.
func TestPostgresDriver_Open_RejectsInsecureSkipVerifyUnderEscalation(t *testing.T) {
	for _, mode := range []string{"disable", "prefer"} {
		t.Run(mode, func(t *testing.T) {
			cfg := sqldb.Config{
				Host: "127.0.0.1", Port: 1,
				User: "u", Password: "p", Name: "db",
				Options: map[string]string{"sslmode": mode},
			}
			tlsCfg := &tls.Config{InsecureSkipVerify: true}
			_, err := PostgresDriver{}.Open(cfg, sqldb.DefaultPool(), slog.Default(), tlsCfg)
			if err == nil {
				t.Fatalf("expected error for InsecureSkipVerify with sslmode=%s + TLS", mode)
			}
			if !strings.Contains(err.Error(), "InsecureSkipVerify") {
				t.Errorf("error %q should mention InsecureSkipVerify", err.Error())
			}
		})
	}
}

// TestPostgresDriver_Open_NilLoggerDoesNotPanic verifies that a nil *slog.Logger
// is normalized at entry rather than panicking on the first logger.Info call.
func TestPostgresDriver_Open_NilLoggerDoesNotPanic(t *testing.T) {
	cfg := sqldb.Config{
		Host: "127.0.0.1", Port: 1,
		User: "u", Password: "p", Name: "db",
		Options: map[string]string{"sslmode": "disable"},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Open panicked with nil logger: %v", r)
		}
	}()
	_, err := PostgresDriver{}.Open(cfg, sqldb.DefaultPool(), nil, nil)
	if err == nil {
		t.Fatal("expected open to fail against unreachable host")
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
