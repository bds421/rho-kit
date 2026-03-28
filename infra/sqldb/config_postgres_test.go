package sqldb

import (
	"log/slog"
	"strings"
	"testing"
)

// Verify PostgresConfig implements slog.LogValuer.
var _ slog.LogValuer = PostgresConfig{}

func TestPostgresConfig_DSN_Defaults(t *testing.T) {
	cfg := PostgresConfig{
		Host: "pg-host", Port: 5432, User: "pguser",
		Password: "pgpass", Name: "pgdb",
	}
	dsn := cfg.DSN()
	expected := "host='pg-host' port=5432 user='pguser' password='pgpass' dbname='pgdb' sslmode='disable'"
	if dsn != expected {
		t.Errorf("DSN() = %q, want %q", dsn, expected)
	}
}

func TestPostgresConfig_DSN_TLSEnabled(t *testing.T) {
	cfg := PostgresConfig{
		Host: "pg-host", Port: 5432, User: "pguser",
		Password: "pgpass", Name: "pgdb",
	}
	dsn := cfg.DSN(true)
	expected := "host='pg-host' port=5432 user='pguser' password='pgpass' dbname='pgdb' sslmode='verify-full'"
	if dsn != expected {
		t.Errorf("DSN(true) = %q, want %q", dsn, expected)
	}
}

func TestPostgresConfig_DSN_ExplicitSSLMode(t *testing.T) {
	cfg := PostgresConfig{
		Host: "pg-host", Port: 5432, User: "pguser",
		Password: "pgpass", Name: "pgdb", SSLMode: "require",
	}
	// Explicit SSLMode should be used regardless of tlsEnabled.
	dsn := cfg.DSN(true)
	expected := "host='pg-host' port=5432 user='pguser' password='pgpass' dbname='pgdb' sslmode='require'"
	if dsn != expected {
		t.Errorf("DSN(true) with SSLMode = %q, want %q", dsn, expected)
	}
}

func TestPostgresConfig_DSN_SpecialCharsEscaped(t *testing.T) {
	cfg := PostgresConfig{
		Host: "pg-host", Port: 5432, User: "pguser",
		Password: "p'ass\\word", Name: "pgdb",
	}
	dsn := cfg.DSN()
	expected := `host='pg-host' port=5432 user='pguser' password='p\'ass\\word' dbname='pgdb' sslmode='disable'`
	if dsn != expected {
		t.Errorf("DSN() = %q, want %q", dsn, expected)
	}
}

func TestPostgresConfig_LogValue_RedactsPassword(t *testing.T) {
	cfg := PostgresConfig{
		Host: "pg-host", Port: 5432, User: "pguser",
		Password: "supersecret", Name: "pgdb",
	}
	val := cfg.LogValue()
	resolved := val.Resolve()
	for _, attr := range resolved.Group() {
		if attr.Key == "password" && attr.Value.String() != "[REDACTED]" {
			t.Errorf("password not redacted: %s", attr.Value.String())
		}
		if attr.Value.String() == "supersecret" {
			t.Error("password leaked in log value")
		}
	}
}

func TestMySQLConfig_DSN_TLSEnabled(t *testing.T) {
	cfg := MySQLConfig{
		Host: "localhost", Port: 3306, User: "root",
		Password: "secret", Name: "mydb",
	}
	dsn := cfg.DSN(true)
	if want := "&tls=custom"; !strings.Contains(dsn, want) {
		t.Errorf("DSN(true) = %q, want to contain %q", dsn, want)
	}
}

func TestMySQLConfig_DSN_SpecialCharsEscaped(t *testing.T) {
	cfg := MySQLConfig{
		Host: "localhost", Port: 3306,
		User: "user@name", Password: "p@ss/word",
		Name: "mydb",
	}
	dsn := cfg.DSN()
	// url.PathEscape escapes @ and /
	if strings.Contains(dsn, "user@name:p@ss/word") {
		t.Errorf("special chars not escaped in DSN: %s", dsn)
	}
}

func TestLoadPool_Defaults(t *testing.T) {
	pool, err := LoadPool(5, 50)
	if err != nil {
		t.Fatalf("LoadPool: %v", err)
	}
	if pool.MaxIdleConns != 5 {
		t.Errorf("MaxIdleConns = %d, want 5", pool.MaxIdleConns)
	}
	if pool.MaxOpenConns != 50 {
		t.Errorf("MaxOpenConns = %d, want 50", pool.MaxOpenConns)
	}
}

func TestLoadPool_EnvOverrides(t *testing.T) {
	t.Setenv("DB_POOL_MAX_IDLE_CONNS", "20")
	t.Setenv("DB_POOL_MAX_OPEN_CONNS", "200")
	t.Setenv("DB_POOL_CONN_MAX_LIFETIME_MIN", "120")
	t.Setenv("DB_POOL_CONN_MAX_IDLE_TIME_MIN", "10")

	pool, err := LoadPool(5, 50)
	if err != nil {
		t.Fatalf("LoadPool: %v", err)
	}
	if pool.MaxIdleConns != 20 {
		t.Errorf("MaxIdleConns = %d, want 20", pool.MaxIdleConns)
	}
	if pool.MaxOpenConns != 200 {
		t.Errorf("MaxOpenConns = %d, want 200", pool.MaxOpenConns)
	}
}

func TestLoadPool_InvalidEnv(t *testing.T) {
	t.Setenv("DB_POOL_MAX_IDLE_CONNS", "not-a-number")
	_, err := LoadPool(5, 50)
	if err == nil {
		t.Fatal("expected error for invalid env var")
	}
}

func TestLoadPostgresFields_Defaults(t *testing.T) {
	t.Setenv("DB_HOST", "pg-host")
	t.Setenv("SERVICE_DB_USER", "pguser")
	t.Setenv("SERVICE_DB_PASSWORD", "supersecretpass")
	t.Setenv("SERVICE_DB_NAME", "pgdb")

	fields, err := LoadPostgresFields("SERVICE", 5, 50)
	if err != nil {
		t.Fatalf("LoadPostgresFields: %v", err)
	}
	if fields.Database.Port != 5432 {
		t.Errorf("port = %d, want 5432", fields.Database.Port)
	}
	if fields.Database.LogLevel != "warn" {
		t.Errorf("log level = %q, want \"warn\"", fields.Database.LogLevel)
	}
	if fields.Database.SSLMode != "" {
		t.Errorf("ssl mode = %q, want empty", fields.Database.SSLMode)
	}
	if fields.DatabasePool.MaxIdleConns != 5 {
		t.Errorf("pool max idle = %d, want 5", fields.DatabasePool.MaxIdleConns)
	}
	if fields.DatabasePool.MaxOpenConns != 50 {
		t.Errorf("pool max open = %d, want 50", fields.DatabasePool.MaxOpenConns)
	}
}

func TestValidatePostgres_InvalidSSLMode(t *testing.T) {
	fields := PostgresFields{
		Database: PostgresConfig{
			Host:     "pg-host",
			Port:     5432,
			User:     "pguser",
			Password: "supersecretpass",
			Name:     "pgdb",
			SSLMode:  "invalid",
		},
	}
	if err := fields.ValidatePostgres("SERVICE", "development"); err == nil {
		t.Fatal("expected invalid SSL mode error")
	}
}
