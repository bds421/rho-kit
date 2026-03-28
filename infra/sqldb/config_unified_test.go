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
		Host:     "localhost",
		Port:     3306,
		User:     "root",
		Password: "supersecret",
		Name:     "mydb",
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

func TestConfig_MySQLDSN(t *testing.T) {
	cfg := Config{
		Host: "localhost", Port: 3306, User: "root",
		Password: "secret", Name: "mydb",
	}
	dsn := cfg.MySQLDSN()
	assert.Contains(t, dsn, "root:secret@tcp(localhost:3306)/mydb")
	assert.Contains(t, dsn, "charset=utf8mb4")
	assert.Contains(t, dsn, "parseTime=True")
}

func TestConfig_MySQLDSN_TLS(t *testing.T) {
	cfg := Config{
		Host: "localhost", Port: 3306, User: "root",
		Password: "secret", Name: "mydb",
	}
	dsn := cfg.MySQLDSN(true)
	assert.Contains(t, dsn, "&tls=custom")
}

func TestConfig_MySQLDSN_TLSString(t *testing.T) {
	cfg := Config{
		Host: "localhost", Port: 3306, User: "root",
		Password: "secret", Name: "mydb",
	}
	dsn := cfg.MySQLDSN("custom-1")
	assert.Contains(t, dsn, "&tls=custom-1")
}

func TestConfig_MySQLDSN_SpecialChars(t *testing.T) {
	cfg := Config{
		Host: "localhost", Port: 3306,
		User: "user@org", Password: "p@ss/w0rd", Name: "mydb",
	}
	dsn := cfg.MySQLDSN()
	assert.NotContains(t, dsn, "user@org:p@ss")
	assert.Contains(t, dsn, "tcp(localhost:3306)/mydb")
}

func TestConfig_DSN_IsAlias(t *testing.T) {
	cfg := Config{
		Host: "localhost", Port: 3306, User: "root",
		Password: "secret", Name: "mydb",
	}
	assert.Equal(t, cfg.MySQLDSN(), cfg.DSN())
}

func TestConfig_PostgresDSN_Defaults(t *testing.T) {
	cfg := Config{
		Host: "pg-host", Port: 5432, User: "pguser",
		Password: "pgpass", Name: "pgdb",
	}
	dsn := cfg.PostgresDSN()
	expected := "host='pg-host' port=5432 user='pguser' password='pgpass' dbname='pgdb' sslmode='disable'"
	assert.Equal(t, expected, dsn)
}

func TestConfig_PostgresDSN_TLSEnabled(t *testing.T) {
	cfg := Config{
		Host: "pg-host", Port: 5432, User: "pguser",
		Password: "pgpass", Name: "pgdb",
	}
	dsn := cfg.PostgresDSN(true)
	assert.Contains(t, dsn, "sslmode='verify-full'")
}

func TestConfig_PostgresDSN_ExplicitSSLMode(t *testing.T) {
	cfg := Config{
		Host: "pg-host", Port: 5432, User: "pguser",
		Password: "pgpass", Name: "pgdb", SSLMode: "require",
	}
	dsn := cfg.PostgresDSN(true)
	assert.Contains(t, dsn, "sslmode='require'")
}

func TestConfig_PostgresDSN_SpecialCharsEscaped(t *testing.T) {
	cfg := Config{
		Host: "pg-host", Port: 5432, User: "pguser",
		Password: "p'ass\\word", Name: "pgdb",
	}
	dsn := cfg.PostgresDSN()
	expected := `host='pg-host' port=5432 user='pguser' password='p\'ass\\word' dbname='pgdb' sslmode='disable'`
	assert.Equal(t, expected, dsn)
}

func TestParsePostgresDSN_Unified(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		want     Config
		wantErr  bool
		errMatch string
	}{
		{
			name:  "standard URL",
			input: "postgres://pguser:pgpass@pg-host:5432/pgdb?sslmode=require",
			want:  Config{Host: "pg-host", Port: 5432, User: "pguser", Password: "pgpass", Name: "pgdb", SSLMode: "require"},
		},
		{
			name:  "postgresql scheme",
			input: "postgresql://user:pass@host/db",
			want:  Config{Host: "host", Port: 5432, User: "user", Password: "pass", Name: "db"},
		},
		{
			name:  "special characters in password",
			input: "postgres://user:p%40ss%2Fw0rd@host:5432/db",
			want:  Config{Host: "host", Port: 5432, User: "user", Password: "p@ss/w0rd", Name: "db"},
		},
		{
			name:  "default port",
			input: "postgres://u:p@h/db",
			want:  Config{Host: "h", Port: 5432, User: "u", Password: "p", Name: "db"},
		},
		{
			name:     "wrong scheme",
			input:    "mysql://user:pass@host/db",
			wantErr:  true,
			errMatch: "scheme must be postgres",
		},
		{
			name:     "invalid URL",
			input:    "://broken",
			wantErr:  true,
			errMatch: "parse DATABASE_URL",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePostgresDSN(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errMatch != "" {
					assert.Contains(t, err.Error(), tt.errMatch)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseMySQLDSN_Unified(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		want     Config
		wantErr  bool
		errMatch string
	}{
		{
			name:  "standard URL",
			input: "mysql://myuser:mypass@db-host:3306/mydb",
			want:  Config{Host: "db-host", Port: 3306, User: "myuser", Password: "mypass", Name: "mydb"},
		},
		{
			name:  "special characters in password",
			input: "mysql://user:p%40ss%2Fw0rd@host:3306/db",
			want:  Config{Host: "host", Port: 3306, User: "user", Password: "p@ss/w0rd", Name: "db"},
		},
		{
			name:  "default port",
			input: "mysql://u:p@h/db",
			want:  Config{Host: "h", Port: 3306, User: "u", Password: "p", Name: "db"},
		},
		{
			name:     "wrong scheme",
			input:    "postgres://user:pass@host/db",
			wantErr:  true,
			errMatch: "scheme must be mysql",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseMySQLDSN(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errMatch != "" {
					assert.Contains(t, err.Error(), tt.errMatch)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
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
	assert.Equal(t, "testpass", f.Database.Password)
	assert.Equal(t, "testdb", f.Database.Name)
	assert.Equal(t, "warn", f.Database.LogLevel)
	assert.Equal(t, 10, f.DatabasePool.MaxIdleConns)
	assert.Equal(t, 100, f.DatabasePool.MaxOpenConns)
}

func TestLoadFields_Postgres_SSLMode(t *testing.T) {
	t.Setenv("SVC_DB_USER", "pguser")
	t.Setenv("SVC_DB_PASSWORD", "pgpass")
	t.Setenv("SVC_DB_NAME", "pgdb")
	t.Setenv("DB_SSL_MODE", "require")

	f, err := LoadFields("SVC", 5432, "postgres", 5, 50)
	require.NoError(t, err)
	assert.Equal(t, "require", f.Database.SSLMode)
}

func TestLoadFields_MySQL_NoSSLMode(t *testing.T) {
	t.Setenv("SVC_DB_USER", "myuser")
	t.Setenv("SVC_DB_PASSWORD", "mypass")
	t.Setenv("SVC_DB_NAME", "mydb")
	t.Setenv("DB_SSL_MODE", "require") // should be ignored for mysql

	f, err := LoadFields("SVC", 3306, "mysql", 5, 50)
	require.NoError(t, err)
	assert.Empty(t, f.Database.SSLMode)
}

func TestLoadFields_DATABASE_URL_Postgres(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://pguser:pgpass@pg-host:5432/pgdb?sslmode=require")
	t.Setenv("DB_LOG_LEVEL", "info")

	f, err := LoadFields("SVC", 5432, "postgres", 5, 50)
	require.NoError(t, err)
	assert.Equal(t, "pg-host", f.Database.Host)
	assert.Equal(t, 5432, f.Database.Port)
	assert.Equal(t, "pguser", f.Database.User)
	assert.Equal(t, "pgpass", f.Database.Password)
	assert.Equal(t, "pgdb", f.Database.Name)
	assert.Equal(t, "require", f.Database.SSLMode)
	assert.Equal(t, "info", f.Database.LogLevel)
}

func TestLoadFields_DATABASE_URL_MySQL(t *testing.T) {
	t.Setenv("DATABASE_URL", "mysql://myuser:mypass@db-host:3306/mydb")
	t.Setenv("DB_LOG_LEVEL", "info")

	f, err := LoadFields("SVC", 3306, "mysql", 5, 50)
	require.NoError(t, err)
	assert.Equal(t, "db-host", f.Database.Host)
	assert.Equal(t, "myuser", f.Database.User)
	assert.Equal(t, "info", f.Database.LogLevel)
}

func TestLoadFields_DATABASE_URL_Precedence(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://urluser:urlpass@urlhost:5432/urldb")
	t.Setenv("DB_HOST", "field-host")
	t.Setenv("SVC_DB_USER", "field-user")

	f, err := LoadFields("SVC", 5432, "postgres", 10, 100)
	require.NoError(t, err)
	assert.Equal(t, "urlhost", f.Database.Host)
	assert.Equal(t, "urluser", f.Database.User)
}

func TestFields_Validate_Valid(t *testing.T) {
	f := Fields{
		Database: Config{
			Host:     "localhost",
			Port:     3306,
			User:     "testuser",
			Password: "a-strong-password-here",
			Name:     "testdb",
		},
	}
	err := f.Validate("BACKEND", "development", "mysql")
	require.NoError(t, err)
}

func TestFields_Validate_MissingHost(t *testing.T) {
	f := Fields{
		Database: Config{Port: 3306, User: "u", Password: "p", Name: "n"},
	}
	err := f.Validate("BACKEND", "development", "mysql")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_HOST is required")
}

func TestFields_Validate_MissingUser(t *testing.T) {
	f := Fields{
		Database: Config{Host: "h", Port: 3306, Password: "p", Name: "n"},
	}
	err := f.Validate("BACKEND", "development", "mysql")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_USER is required")
}

func TestFields_Validate_MissingPassword(t *testing.T) {
	f := Fields{
		Database: Config{Host: "h", Port: 3306, User: "u", Name: "n"},
	}
	err := f.Validate("BACKEND", "development", "mysql")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_PASSWORD is required")
}

func TestFields_Validate_MissingName(t *testing.T) {
	f := Fields{
		Database: Config{Host: "h", Port: 3306, User: "u", Password: "p"},
	}
	err := f.Validate("BACKEND", "development", "mysql")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_NAME is required")
}

func TestFields_Validate_WeakPasswordInProduction(t *testing.T) {
	f := Fields{
		Database: Config{Host: "h", Port: 3306, User: "u", Password: "pw", Name: "n"},
	}
	err := f.Validate("BACKEND", "production", "mysql")
	require.Error(t, err)
}

func TestFields_Validate_InvalidSSLMode(t *testing.T) {
	f := Fields{
		Database: Config{
			Host: "h", Port: 5432, User: "u",
			Password: "a-strong-password-here", Name: "n",
			SSLMode: "invalid",
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
			SSLMode: "require",
		},
	}
	err := f.Validate("SVC", "development", "postgres")
	require.NoError(t, err)
}

func TestFields_Validate_MySQL_IgnoresSSLMode(t *testing.T) {
	f := Fields{
		Database: Config{
			Host: "h", Port: 3306, User: "u",
			Password: "a-strong-password-here", Name: "n",
			SSLMode: "invalid", // should be ignored for MySQL
		},
	}
	err := f.Validate("SVC", "development", "mysql")
	require.NoError(t, err)
}
