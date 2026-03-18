package config

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type basicConfig struct {
	Host    string        `env:"TEST_HOST" default:"localhost"`
	Port    int           `env:"TEST_PORT" default:"8080"`
	Debug   bool          `env:"TEST_DEBUG"`
	Timeout time.Duration `env:"TEST_TIMEOUT" default:"30s"`
	Tags    []string      `env:"TEST_TAGS"`
	Rate    float64       `env:"TEST_RATE"`
	PortU16 uint16        `env:"TEST_PORT_U16"`
}

func clearEnv(keys ...string) {
	for _, k := range keys {
		_ = os.Unsetenv(k)
	}
}

func TestLoad_Defaults(t *testing.T) {
	clearEnv("TEST_HOST", "TEST_PORT", "TEST_DEBUG", "TEST_TIMEOUT", "TEST_TAGS", "TEST_RATE", "TEST_PORT_U16")

	cfg, err := Load[basicConfig]()
	require.NoError(t, err)
	assert.Equal(t, "localhost", cfg.Host)
	assert.Equal(t, 8080, cfg.Port)
	assert.False(t, cfg.Debug)
	assert.Equal(t, 30*time.Second, cfg.Timeout)
}

func TestLoad_EnvOverridesDefault(t *testing.T) {
	t.Setenv("TEST_HOST", "example.com")
	t.Setenv("TEST_PORT", "9090")
	t.Setenv("TEST_DEBUG", "true")
	t.Setenv("TEST_TIMEOUT", "5s")
	t.Setenv("TEST_TAGS", "a, b, c")
	t.Setenv("TEST_RATE", "1.5")
	t.Setenv("TEST_PORT_U16", "443")

	cfg, err := Load[basicConfig]()
	require.NoError(t, err)
	assert.Equal(t, "example.com", cfg.Host)
	assert.Equal(t, 9090, cfg.Port)
	assert.True(t, cfg.Debug)
	assert.Equal(t, 5*time.Second, cfg.Timeout)
	assert.Equal(t, []string{"a", "b", "c"}, cfg.Tags)
	assert.InDelta(t, 1.5, cfg.Rate, 0.001)
	assert.Equal(t, uint16(443), cfg.PortU16)
}

func TestLoad_Required(t *testing.T) {
	type cfg struct {
		Secret string `env:"TEST_REQUIRED,required"`
	}
	clearEnv("TEST_REQUIRED")

	_, err := Load[cfg]()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_REQUIRED")
	assert.Contains(t, err.Error(), "required")
}

func TestLoad_SecretFile(t *testing.T) {
	type cfg struct {
		Password string `env:"TEST_PASSWORD" secret:"true"`
	}
	clearEnv("TEST_PASSWORD", "TEST_PASSWORD_FILE")

	tmpDir := t.TempDir()
	secretFile := filepath.Join(tmpDir, "password.txt")
	require.NoError(t, os.WriteFile(secretFile, []byte("s3cr3t\n"), 0600))
	t.Setenv("TEST_PASSWORD_FILE", secretFile)

	c, err := Load[cfg]()
	require.NoError(t, err)
	assert.Equal(t, "s3cr3t", c.Password)
}

func TestLoad_EnvTakesPrecedenceOverFile(t *testing.T) {
	type cfg struct {
		Password string `env:"TEST_PASSWORD2" secret:"true"`
	}
	t.Setenv("TEST_PASSWORD2", "direct-value")
	t.Setenv("TEST_PASSWORD2_FILE", "/nonexistent")

	c, err := Load[cfg]()
	require.NoError(t, err)
	assert.Equal(t, "direct-value", c.Password)
}

func TestLoad_NestedStruct(t *testing.T) {
	type DB struct {
		Host string `env:"TEST_DB_HOST" default:"localhost"`
		Port int    `env:"TEST_DB_PORT" default:"5432"`
	}
	type cfg struct {
		Name     string `env:"TEST_APP_NAME" default:"myapp"`
		Database DB
	}
	clearEnv("TEST_APP_NAME", "TEST_DB_HOST", "TEST_DB_PORT")

	c, err := Load[cfg]()
	require.NoError(t, err)
	assert.Equal(t, "myapp", c.Name)
	assert.Equal(t, "localhost", c.Database.Host)
	assert.Equal(t, 5432, c.Database.Port)
}

func TestLoad_InvalidInt(t *testing.T) {
	type cfg struct {
		Port int `env:"TEST_BAD_INT"`
	}
	t.Setenv("TEST_BAD_INT", "not-a-number")

	_, err := Load[cfg]()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_BAD_INT")
}

func TestLoad_InvalidBool(t *testing.T) {
	type cfg struct {
		Flag bool `env:"TEST_BAD_BOOL"`
	}
	t.Setenv("TEST_BAD_BOOL", "maybe")

	_, err := Load[cfg]()
	assert.Error(t, err)
}

func TestLoad_URLField(t *testing.T) {
	type cfg struct {
		Endpoint *url.URL `env:"TEST_URL"`
	}
	t.Setenv("TEST_URL", "https://example.com/api")

	c, err := Load[cfg]()
	require.NoError(t, err)
	assert.Equal(t, "https://example.com/api", c.Endpoint.String())
}

func TestMustLoad_Panics(t *testing.T) {
	type cfg struct {
		X string `env:"TEST_MUST_PANIC,required"`
	}
	clearEnv("TEST_MUST_PANIC")

	assert.Panics(t, func() { MustLoad[cfg]() })
}

func TestLoad_SecretFileMissing(t *testing.T) {
	type cfg struct {
		Password string `env:"TEST_MISSING_SECRET" secret:"true"`
	}
	clearEnv("TEST_MISSING_SECRET")
	t.Setenv("TEST_MISSING_SECRET_FILE", "/nonexistent/path/secret.txt")

	_, err := Load[cfg]()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_MISSING_SECRET")
	assert.Contains(t, err.Error(), "/nonexistent/path/secret.txt")
}

func TestLoad_PointerToStructField_NilWhenOnlyDefaults(t *testing.T) {
	type DB struct {
		Host string `env:"TEST_PTR_DB_HOST" default:"dbhost"`
	}
	type cfg struct {
		Database *DB
	}
	clearEnv("TEST_PTR_DB_HOST")

	c, err := Load[cfg]()
	require.NoError(t, err)
	// Pointer stays nil when only defaults are applied — nil-means-disabled convention.
	assert.Nil(t, c.Database)
}

func TestLoad_PointerToStructField_AllocatedWhenEnvSet(t *testing.T) {
	type DB struct {
		Host string `env:"TEST_PTR_DB_HOST" default:"dbhost"`
		Port int    `env:"TEST_PTR_DB_PORT" default:"3306"`
	}
	type cfg struct {
		Database *DB
	}
	t.Setenv("TEST_PTR_DB_HOST", "custom-host")

	c, err := Load[cfg]()
	require.NoError(t, err)
	require.NotNil(t, c.Database)
	assert.Equal(t, "custom-host", c.Database.Host)
	assert.Equal(t, 3306, c.Database.Port) // default still applied
}

func TestLoad_PointerToStructField_NilWhenNoFields(t *testing.T) {
	type Empty struct {
		Host string `env:"TEST_PTR_EMPTY_HOST"`
	}
	type cfg struct {
		Database *Empty
	}
	clearEnv("TEST_PTR_EMPTY_HOST")

	c, err := Load[cfg]()
	require.NoError(t, err)
	assert.Nil(t, c.Database)
}

func TestLoad_IntOverflow(t *testing.T) {
	type cfg struct {
		Val int8 `env:"TEST_INT8_OVERFLOW"`
	}
	t.Setenv("TEST_INT8_OVERFLOW", "200") // > 127

	_, err := Load[cfg]()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_INT8_OVERFLOW")
}

func TestLoad_Uint8Overflow(t *testing.T) {
	type cfg struct {
		Val uint8 `env:"TEST_UINT8_OVERFLOW"`
	}
	t.Setenv("TEST_UINT8_OVERFLOW", "300") // > 255

	_, err := Load[cfg]()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_UINT8_OVERFLOW")
}

func TestLoad_URLWithoutScheme(t *testing.T) {
	type cfg struct {
		Endpoint *url.URL `env:"TEST_URL_NO_SCHEME"`
	}
	t.Setenv("TEST_URL_NO_SCHEME", "not-a-url")

	_, err := Load[cfg]()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "scheme")
}

func TestLoad_UnexportedFieldsSkipped(t *testing.T) {
	type cfg struct {
		Public  string `env:"TEST_PUBLIC" default:"yes"`
		private string `env:"TEST_PRIVATE" default:"no"` //nolint:unused
	}
	c, err := Load[cfg]()
	require.NoError(t, err)
	assert.Equal(t, "yes", c.Public)
}

func TestLoad_NoEnvTags_ReturnsError(t *testing.T) {
	type noTags struct {
		Name string
		Age  int
	}
	_, err := Load[noTags]()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no env struct tags")
}

func TestLoad_NestedEnvTags_Ok(t *testing.T) {
	type inner struct {
		Host string `env:"TEST_NESTED_CHECK_HOST" default:"localhost"`
	}
	type outer struct {
		DB inner
	}
	clearEnv("TEST_NESTED_CHECK_HOST")

	cfg, err := Load[outer]()
	require.NoError(t, err)
	assert.Equal(t, "localhost", cfg.DB.Host)
}
