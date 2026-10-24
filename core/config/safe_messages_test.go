package config

import (
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/redact"
)

func TestLoadErrorsAreSafeToLog(t *testing.T) {
	type cfg struct {
		URL string `env:"TEST_SAFE_URL,required"`
	}
	clearEnv("TEST_SAFE_URL")

	_, err := Load[cfg]()

	require.Error(t, err)
	assert.Equal(t, "config: required environment variable TEST_SAFE_URL is not set", redact.SafeMessages(err))
}

func TestSecretFileErrorNamesVariableNotPath(t *testing.T) {
	type cfg struct {
		Token string `env:"TEST_SAFE_TOKEN" secret:"true"`
	}
	missing := filepath.Join(t.TempDir(), "tenant-a-token")
	t.Setenv("TEST_SAFE_TOKEN_FILE", missing)

	_, err := Load[cfg]()

	require.Error(t, err)
	assert.ErrorIs(t, err, fs.ErrNotExist)
	safe := redact.SafeMessages(err)
	assert.Equal(t, "config: failed to read secret file for TEST_SAFE_TOKEN_FILE: open failed: file does not exist", safe)
	assert.NotContains(t, safe, "tenant-a-token")
}

func TestGetSecretErrorNamesVariableNotPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "tenant-a-token")
	t.Setenv("TEST_SAFE_SECRET_FILE", missing)

	_, err := GetSecret("TEST_SAFE_SECRET", "")

	require.Error(t, err)
	safe := redact.SafeMessages(err)
	assert.Contains(t, safe, "TEST_SAFE_SECRET_FILE is unreadable")
	assert.NotContains(t, safe, "tenant-a-token")
}
