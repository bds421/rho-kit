package sqldb

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ slog.LogValuer = Config{}

func TestConfigLogValueRedactsConnectionTopology(t *testing.T) {
	cfg := Config{
		Host:     "tenant-db.internal",
		Port:     5432,
		User:     "tenant-user",
		Password: "db-secret-password",
		Name:     "tenant_database",
		LogLevel: "warn",
		Options:  map[string]string{"sslmode": "verify-full", "application_name": "tenant-app"},
	}

	rendered := cfg.LogValue().String()
	for _, forbidden := range []string{
		cfg.Host,
		cfg.User,
		cfg.Password,
		cfg.Name,
		"tenant-app",
		"verify-full",
	} {
		assert.NotContains(t, rendered, forbidden)
	}
	assert.Contains(t, rendered, "host_configured=true")
	assert.Contains(t, rendered, "user_configured=true")
	assert.Contains(t, rendered, "name_configured=true")
	assert.Contains(t, rendered, "password_configured=true")
	assert.Contains(t, rendered, "options_configured=true")
	assert.Contains(t, rendered, "tls_enabled=true")
}

func TestParseDSN(t *testing.T) {
	t.Run("valid postgres URL", func(t *testing.T) {
		cfg, err := ParseDSN("postgres://user:pass@db.example.com:5433/app?sslmode=verify-full")
		require.NoError(t, err)

		assert.Equal(t, "db.example.com", cfg.Host)
		assert.Equal(t, 5433, cfg.Port)
		assert.Equal(t, "user", cfg.User)
		assert.Equal(t, "pass", cfg.Password)
		assert.Equal(t, "app", cfg.Name)
		assert.Equal(t, "verify-full", cfg.Option("sslmode", ""))
	})

	t.Run("defaults port", func(t *testing.T) {
		cfg, err := ParseDSN("postgresql://user:pass@db.example.com/app")
		require.NoError(t, err)

		assert.Equal(t, 5432, cfg.Port)
	})

	t.Run("rejects unsupported scheme", func(t *testing.T) {
		_, err := ParseDSN("secret-token-scheme://user:pass@db.example.com/app")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "scheme")
		assert.NotContains(t, err.Error(), "secret-token-scheme")
	})

	t.Run("rejects missing host", func(t *testing.T) {
		_, err := ParseDSN("postgres:///app?sslmode=verify-full")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "host")
	})

	t.Run("rejects missing database name", func(t *testing.T) {
		_, err := ParseDSN("postgres://user:pass@db.example.com/")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "database name")
	})

	t.Run("rejects invalid port", func(t *testing.T) {
		_, err := ParseDSN("postgres://user:pass@db.example.com:bad/app")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid port")
	})

	t.Run("rejects duplicate sslmode", func(t *testing.T) {
		_, err := ParseDSN("postgres://user:pass@db.example.com/app?sslmode=verify-full&sslmode=disable")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "sslmode")
	})

	t.Run("parse error does not echo value", func(t *testing.T) {
		_, err := ParseDSN("postgres://user:pass@db.example.com/%zz?token=secret-token")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "DATABASE_URL is invalid")
		assert.NotContains(t, err.Error(), "secret-token")
		assert.NotContains(t, err.Error(), "token=")
		assert.NotContains(t, err.Error(), "%zz")
	})
}

func TestFieldsValidate_SSLModeErrorsDoNotEchoValue(t *testing.T) {
	cases := map[string]string{
		"plaintext fallback": "prefer",
		"disable":            "disable",
		"unsupported":        "secret-token-mode",
	}
	for name, sslMode := range cases {
		t.Run(name, func(t *testing.T) {
			fields := Fields{
				Database: Config{
					Host:     "db.example.com",
					User:     "app",
					Password: "correct-horse-battery-staple",
					Name:     "app",
					Options:  map[string]string{"sslmode": sslMode},
				},
			}

			err := fields.Validate("APP")
			require.Error(t, err)
			assert.NotContains(t, err.Error(), sslMode)
			assert.NotContains(t, err.Error(), "secret-token")
		})
	}
}

func TestFieldsValidate_DBHostInvalidCharacterDoesNotEchoValue(t *testing.T) {
	fields := Fields{
		Database: Config{
			Host:     "db/secret-token.example",
			Port:     5432,
			User:     "app",
			Password: "correct-horse-battery-staple",
			Name:     "app",
			Options:  map[string]string{"sslmode": "verify-full"},
		},
	}

	err := fields.Validate("APP")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DB_HOST contains invalid character")
	assert.NotContains(t, err.Error(), "/")
	assert.NotContains(t, err.Error(), "secret-token")
}
