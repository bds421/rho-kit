package sqldb

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Verify deprecated config types still implement slog.LogValuer.
var _ slog.LogValuer = MySQLConfig{}

func TestMySQLConfig_DSN(t *testing.T) {
	cfg := MySQLConfig{
		Host:     "localhost",
		Port:     3306,
		User:     "root",
		Password: "secret",
		Name:     "mydb",
	}
	dsn := cfg.DSN()
	assert.Contains(t, dsn, "root:secret@tcp(localhost:3306)/mydb")
	assert.Contains(t, dsn, "charset=utf8mb4")
	assert.Contains(t, dsn, "parseTime=True")
}

func TestMySQLConfig_DSN_SpecialChars(t *testing.T) {
	cfg := MySQLConfig{
		Host:     "localhost",
		Port:     3306,
		User:     "user@org",
		Password: "p@ss/w0rd",
		Name:     "mydb",
	}
	dsn := cfg.DSN()
	// @ and / in credentials must be encoded so DSN parsing doesn't break
	assert.NotContains(t, dsn, "user@org:p@ss")
	assert.Contains(t, dsn, "tcp(localhost:3306)/mydb")
}

func TestMySQLConfig_DSN_TLS(t *testing.T) {
	cfg := MySQLConfig{
		Host:     "localhost",
		Port:     3306,
		User:     "root",
		Password: "secret",
		Name:     "mydb",
	}
	dsn := cfg.DSN(true)
	assert.Contains(t, dsn, "&tls=custom")
}

func TestMySQLConfig_LogValue_RedactsPassword(t *testing.T) {
	cfg := MySQLConfig{
		Host:     "localhost",
		Port:     3306,
		User:     "root",
		Password: "supersecret",
		Name:     "mydb",
	}
	val := cfg.LogValue()
	resolved := val.Resolve()
	attrs := resolved.Group()
	for _, attr := range attrs {
		if attr.Key == "password" {
			assert.Equal(t, "[REDACTED]", attr.Value.String())
		}
		assert.NotContains(t, attr.Value.String(), "supersecret")
	}
}

func TestDefaultPool(t *testing.T) {
	pool := DefaultPool()
	assert.Equal(t, 10, pool.MaxIdleConns)
	assert.Equal(t, 100, pool.MaxOpenConns)
}

func TestLoadMySQLFields(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("BACKEND_DB_USER", "testuser")
		t.Setenv("BACKEND_DB_PASSWORD", "testpass")
		t.Setenv("BACKEND_DB_NAME", "testdb")

		f, err := LoadMySQLFields("BACKEND", 10, 100)
		if err != nil {
			t.Fatal(err)
		}
		if f.Database.Host != "localhost" {
			t.Errorf("host = %q, want localhost", f.Database.Host)
		}
		if f.Database.Port != 3306 {
			t.Errorf("port = %d, want 3306", f.Database.Port)
		}
		if f.Database.User != "testuser" {
			t.Errorf("user = %q, want testuser", f.Database.User)
		}
		if f.Database.Password != "testpass" {
			t.Errorf("password = %q, want testpass", f.Database.Password)
		}
		if f.Database.Name != "testdb" {
			t.Errorf("name = %q, want testdb", f.Database.Name)
		}
		if f.Database.LogLevel != "warn" {
			t.Errorf("log_level = %q, want warn", f.Database.LogLevel)
		}
		if f.DatabasePool.MaxIdleConns != 10 {
			t.Errorf("max_idle = %d, want 10", f.DatabasePool.MaxIdleConns)
		}
		if f.DatabasePool.MaxOpenConns != 100 {
			t.Errorf("max_open = %d, want 100", f.DatabasePool.MaxOpenConns)
		}
	})

	t.Run("different prefix", func(t *testing.T) {
		t.Setenv("FILECOPIER_DB_USER", "fcuser")
		t.Setenv("FILECOPIER_DB_PASSWORD", "fcpass")
		t.Setenv("FILECOPIER_DB_NAME", "fcdb")

		f, err := LoadMySQLFields("FILECOPIER", 5, 25)
		if err != nil {
			t.Fatal(err)
		}
		if f.Database.User != "fcuser" {
			t.Errorf("user = %q, want fcuser", f.Database.User)
		}
		if f.DatabasePool.MaxIdleConns != 5 {
			t.Errorf("max_idle = %d, want 5", f.DatabasePool.MaxIdleConns)
		}
	})
}

func TestMySQLFields_ValidateMySQL(t *testing.T) {
	validFields := MySQLFields{
		Database: MySQLConfig{
			Host:     "localhost",
			Port:     3306,
			User:     "testuser",
			Password: "a-strong-password-here",
			Name:     "testdb",
		},
	}

	t.Run("valid", func(t *testing.T) {
		if err := validFields.ValidateMySQL("BACKEND", "development"); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("missing host", func(t *testing.T) {
		f := validFields
		f.Database.Host = ""
		if err := f.ValidateMySQL("BACKEND", "development"); err == nil {
			t.Error("expected error for empty host")
		}
	})

	t.Run("missing user", func(t *testing.T) {
		f := validFields
		f.Database.User = ""
		if err := f.ValidateMySQL("BACKEND", "development"); err == nil {
			t.Error("expected error for empty user")
		}
	})

	t.Run("missing password", func(t *testing.T) {
		f := validFields
		f.Database.Password = ""
		if err := f.ValidateMySQL("BACKEND", "development"); err == nil {
			t.Error("expected error for empty password")
		}
	})

	t.Run("missing name", func(t *testing.T) {
		f := validFields
		f.Database.Name = ""
		if err := f.ValidateMySQL("BACKEND", "development"); err == nil {
			t.Error("expected error for empty name")
		}
	})

	t.Run("weak password in production", func(t *testing.T) {
		f := validFields
		f.Database.Password = "pw"
		err := f.ValidateMySQL("BACKEND", "production")
		if err == nil {
			t.Error("expected error for weak password in production")
		}
	})
}

func TestParsePostgresDSN_Compat(t *testing.T) {
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
			want:  Config{Host: "pg-host", Port: 5432, User: "pguser", Password: "pgpass", Name: "pgdb", Options: map[string]string{"sslmode": "require"}},
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
			name:  "no sslmode",
			input: "postgres://u:p@h:5432/db",
			want:  Config{Host: "h", Port: 5432, User: "u", Password: "p", Name: "db"},
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

func TestParseMySQLDSN_Compat(t *testing.T) {
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
			name:  "custom port",
			input: "mysql://u:p@h:3307/db",
			want:  Config{Host: "h", Port: 3307, User: "u", Password: "p", Name: "db"},
		},
		{
			name:     "wrong scheme",
			input:    "postgres://user:pass@host/db",
			wantErr:  true,
			errMatch: "scheme must be mysql",
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

func TestLoadPostgresFields_DATABASE_URL(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://pguser:pgpass@pg-host:5432/pgdb?sslmode=require")
	t.Setenv("DB_LOG_LEVEL", "info")

	f, err := LoadPostgresFields("SVC", 5, 50)
	require.NoError(t, err)
	assert.Equal(t, "pg-host", f.Database.Host)
	assert.Equal(t, 5432, f.Database.Port)
	assert.Equal(t, "pguser", f.Database.User)
	assert.Equal(t, "pgpass", f.Database.Password)
	assert.Equal(t, "pgdb", f.Database.Name)
	assert.Equal(t, "require", f.Database.SSLMode) //nolint:staticcheck // testing deprecated type
	assert.Equal(t, "info", f.Database.LogLevel)
	assert.Equal(t, 5, f.DatabasePool.MaxIdleConns)
}

func TestLoadPostgresFields_DATABASE_URL_Precedence(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://urluser:urlpass@urlhost:5432/urldb")
	t.Setenv("DB_HOST", "field-host")
	t.Setenv("SVC_DB_USER", "field-user")

	f, err := LoadPostgresFields("SVC", 10, 100)
	require.NoError(t, err)
	assert.Equal(t, "urlhost", f.Database.Host)
	assert.Equal(t, "urluser", f.Database.User)
}

func TestLoadMySQLFields_DATABASE_URL(t *testing.T) {
	t.Setenv("DATABASE_URL", "mysql://myuser:mypass@db-host:3306/mydb")
	t.Setenv("DB_LOG_LEVEL", "info")

	f, err := LoadMySQLFields("SVC", 5, 50)
	require.NoError(t, err)
	assert.Equal(t, "db-host", f.Database.Host)
	assert.Equal(t, 3306, f.Database.Port)
	assert.Equal(t, "myuser", f.Database.User)
	assert.Equal(t, "mypass", f.Database.Password)
	assert.Equal(t, "mydb", f.Database.Name)
	assert.Equal(t, "info", f.Database.LogLevel)
}

func TestLoadMySQLFields_DATABASE_URL_Precedence(t *testing.T) {
	t.Setenv("DATABASE_URL", "mysql://urluser:urlpass@urlhost:3306/urldb")
	t.Setenv("DB_HOST", "field-host")
	t.Setenv("BACKEND_DB_USER", "field-user")

	f, err := LoadMySQLFields("BACKEND", 10, 100)
	require.NoError(t, err)
	assert.Equal(t, "urlhost", f.Database.Host)
	assert.Equal(t, "urluser", f.Database.User)
}

