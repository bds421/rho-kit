package sqldb

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Verify Config implements slog.LogValuer.
var _ slog.LogValuer = Config{}

func TestConfig_LogValue_RedactsPassword(t *testing.T) {
	cfg := Config{
		Host: "localhost", Port: 3306, User: "root",
		Password: "supersecret", Name: "mydb",
	}
	val := cfg.LogValue()
	resolved := val.Resolve()
	for _, attr := range resolved.Group() {
		if attr.Key == "password" {
			assert.Equal(t, "[REDACTED]", attr.Value.String())
		}
		assert.NotContains(t, attr.Value.String(), "supersecret")
	}
}

func TestConfig_Option(t *testing.T) {
	cfg := Config{
		Options: map[string]string{"sslmode": "require", "charset": "utf8mb4"},
	}
	assert.Equal(t, "require", cfg.Option("sslmode", ""))
	assert.Equal(t, "utf8mb4", cfg.Option("charset", ""))
	assert.Equal(t, "fallback", cfg.Option("missing", "fallback"))
}

func TestConfig_Option_NilMap(t *testing.T) {
	cfg := Config{}
	assert.Equal(t, "default", cfg.Option("key", "default"))
}

// DSN tests use the deprecated Config.DSN() method (MySQL) and
// PostgresConfig.DSN() (Postgres) which remain for backward compat.

func TestConfig_DSN_MySQL(t *testing.T) {
	cfg := Config{
		Host: "localhost", Port: 3306, User: "root",
		Password: "secret", Name: "mydb",
	}
	dsn := cfg.DSN() //nolint:staticcheck // testing deprecated method
	assert.Contains(t, dsn, "root:secret@tcp(localhost:3306)/mydb")
	assert.Contains(t, dsn, "charset=utf8mb4")
}

func TestConfig_DSN_MySQL_TLS(t *testing.T) {
	cfg := Config{
		Host: "localhost", Port: 3306, User: "root",
		Password: "secret", Name: "mydb",
	}
	dsn := cfg.DSN(true) //nolint:staticcheck // testing deprecated method
	assert.Contains(t, dsn, "&tls=custom")
}

func TestPostgresConfig_DSN(t *testing.T) {
	cfg := PostgresConfig{
		Host: "pg-host", Port: 5432, User: "pguser",
		Password: "pgpass", Name: "pgdb",
	}
	dsn := cfg.DSN() //nolint:staticcheck // testing deprecated method
	expected := "host='pg-host' port=5432 user='pguser' password='pgpass' dbname='pgdb' sslmode='disable'"
	assert.Equal(t, expected, dsn)
}

func TestPostgresConfig_DSN_TLSEnabled(t *testing.T) {
	cfg := PostgresConfig{
		Host: "pg-host", Port: 5432, User: "pguser",
		Password: "pgpass", Name: "pgdb",
	}
	dsn := cfg.DSN(true) //nolint:staticcheck // testing deprecated method
	assert.Contains(t, dsn, "sslmode='verify-full'")
}

func TestPostgresConfig_DSN_ExplicitSSLMode(t *testing.T) {
	cfg := PostgresConfig{
		Host: "pg-host", Port: 5432, User: "pguser",
		Password: "pgpass", Name: "pgdb", SSLMode: "require",
	}
	dsn := cfg.DSN(true) //nolint:staticcheck // testing deprecated method
	assert.Contains(t, dsn, "sslmode='require'")
}

func TestParsePostgresDSN_Unified(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantHost string
		wantPort int
		wantSSL  string
		wantErr  bool
	}{
		{
			name:     "standard URL with sslmode",
			input:    "postgres://pguser:pgpass@pg-host:5432/pgdb?sslmode=require",
			wantHost: "pg-host",
			wantPort: 5432,
			wantSSL:  "require",
		},
		{
			name:     "no sslmode",
			input:    "postgres://user:pass@host/db",
			wantHost: "host",
			wantPort: 5432,
			wantSSL:  "",
		},
		{
			name:    "wrong scheme",
			input:   "mysql://user:pass@host/db",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePostgresDSN(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantHost, got.Host)
			assert.Equal(t, tt.wantPort, got.Port)
			assert.Equal(t, tt.wantSSL, got.Option("sslmode", ""))
		})
	}
}

func TestParseMySQLDSN_Unified(t *testing.T) {
	got, err := ParseMySQLDSN("mysql://myuser:mypass@db-host:3306/mydb")
	require.NoError(t, err)
	assert.Equal(t, "db-host", got.Host)
	assert.Equal(t, 3306, got.Port)
	assert.Equal(t, "myuser", got.User)
}

func TestLoadFields_Defaults(t *testing.T) {
	t.Setenv("BACKEND_DB_USER", "testuser")
	t.Setenv("BACKEND_DB_PASSWORD", "testpass")
	t.Setenv("BACKEND_DB_NAME", "testdb")

	f, err := LoadFields("BACKEND", 3306, "mysql", 10, 100)
	require.NoError(t, err)
	assert.Equal(t, "localhost", f.Database.Host)
	assert.Equal(t, 3306, f.Database.Port)
	assert.Equal(t, "testuser", f.Database.User)
}

func TestLoadFields_Postgres_SSLMode(t *testing.T) {
	t.Setenv("SVC_DB_USER", "pguser")
	t.Setenv("SVC_DB_PASSWORD", "pgpass")
	t.Setenv("SVC_DB_NAME", "pgdb")
	t.Setenv("DB_SSL_MODE", "require")

	f, err := LoadFields("SVC", 5432, "postgres", 5, 50)
	require.NoError(t, err)
	assert.Equal(t, "require", f.Database.Option("sslmode", ""))
}

func TestLoadFields_MySQL_NoSSLMode(t *testing.T) {
	t.Setenv("SVC_DB_USER", "myuser")
	t.Setenv("SVC_DB_PASSWORD", "mypass")
	t.Setenv("SVC_DB_NAME", "mydb")
	t.Setenv("DB_SSL_MODE", "require") // ignored for MySQL

	f, err := LoadFields("SVC", 3306, "mysql", 5, 50)
	require.NoError(t, err)
	assert.Equal(t, "", f.Database.Option("sslmode", ""))
}

func TestLoadFields_DATABASE_URL_Postgres(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://pguser:pgpass@pg-host:5432/pgdb?sslmode=require")
	t.Setenv("DB_LOG_LEVEL", "info")

	f, err := LoadFields("SVC", 5432, "postgres", 5, 50)
	require.NoError(t, err)
	assert.Equal(t, "pg-host", f.Database.Host)
	assert.Equal(t, "require", f.Database.Option("sslmode", ""))
	assert.Equal(t, "info", f.Database.LogLevel)
}

func TestFields_Validate_Valid(t *testing.T) {
	f := Fields{
		Database: Config{
			Host: "localhost", Port: 3306, User: "testuser",
			Password: "a-strong-password-here", Name: "testdb",
		},
	}
	require.NoError(t, f.Validate("BACKEND", "development", "mysql"))
}

func TestFields_Validate_InvalidSSLMode(t *testing.T) {
	f := Fields{
		Database: Config{
			Host: "h", Port: 5432, User: "u",
			Password: "a-strong-password-here", Name: "n",
			Options: map[string]string{"sslmode": "invalid"},
		},
	}
	err := f.Validate("SVC", "development", "postgres")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_SSL_MODE")
}

func TestFields_Validate_ValidSSLMode(t *testing.T) {
	f := Fields{
		Database: Config{
			Host: "h", Port: 5432, User: "u",
			Password: "a-strong-password-here", Name: "n",
			Options: map[string]string{"sslmode": "require"},
		},
	}
	require.NoError(t, f.Validate("SVC", "development", "postgres"))
}

func TestFields_Validate_MySQL_IgnoresSSLMode(t *testing.T) {
	f := Fields{
		Database: Config{
			Host: "h", Port: 3306, User: "u",
			Password: "a-strong-password-here", Name: "n",
			Options: map[string]string{"sslmode": "invalid"},
		},
	}
	require.NoError(t, f.Validate("SVC", "development", "mysql"))
}
