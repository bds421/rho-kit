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

func TestLoad_StringSliceRejectsEmptyElements(t *testing.T) {
	type cfg struct {
		Tags []string `env:"TEST_TAGS_EMPTY_ITEM"`
	}
	t.Setenv("TEST_TAGS_EMPTY_ITEM", "a,,b")

	_, err := Load[cfg]()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_TAGS_EMPTY_ITEM")
	assert.Contains(t, err.Error(), "empty list item")
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

func TestLoad_EnvTagRejectsEmptyName(t *testing.T) {
	type cfg struct {
		Secret string `env:",required"`
	}

	_, err := Load[cfg]()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must name an environment variable")
	assert.NotContains(t, err.Error(), "Secret")
}

func TestLoad_EnvTagRejectsUnknownOption(t *testing.T) {
	type cfg struct {
		Secret string `env:"TEST_UNKNOWN_ENV_OPTION,required,typo"`
	}

	_, err := Load[cfg]()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown option")
	assert.NotContains(t, err.Error(), "Secret")
	assert.NotContains(t, err.Error(), "typo")
}

func TestLoad_RequiredRejectsExplicitEmpty(t *testing.T) {
	// "required" must reject the case where the operator explicitly set the
	// var to "" (empty file, blank export). Falling back to a default in
	// that case defeats the whole point of marking it required.
	type cfg struct {
		Secret string `env:"TEST_REQUIRED_EXPLICIT_EMPTY,required" default:"fallback"`
	}
	t.Setenv("TEST_REQUIRED_EXPLICIT_EMPTY", "")

	_, err := Load[cfg]()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_REQUIRED_EXPLICIT_EMPTY")
	assert.Contains(t, err.Error(), "set but empty")
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

func TestLoad_SecretFileTakesPrecedenceOverEnv(t *testing.T) {
	type cfg struct {
		Password string `env:"TEST_PASSWORD2" secret:"true"`
	}
	tmpDir := t.TempDir()
	secretFile := filepath.Join(tmpDir, "password.txt")
	require.NoError(t, os.WriteFile(secretFile, []byte("file-value\n"), 0600))

	t.Setenv("TEST_PASSWORD2", "direct-value")
	t.Setenv("TEST_PASSWORD2_FILE", secretFile)

	c, err := Load[cfg]()
	require.NoError(t, err)
	assert.Equal(t, "file-value", c.Password)
}

func TestLoad_SecretFileMustBeReadableEvenWhenEnvSet(t *testing.T) {
	type cfg struct {
		Password string `env:"TEST_PASSWORD2_UNREADABLE" secret:"true"`
	}
	t.Setenv("TEST_PASSWORD2_UNREADABLE", "direct-value")
	t.Setenv("TEST_PASSWORD2_UNREADABLE_FILE", "/nonexistent")

	_, err := Load[cfg]()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_PASSWORD2_UNREADABLE")
	assert.NotContains(t, err.Error(), "/nonexistent")
}

func TestLoad_RequiredSecretEmptyFileDoesNotFallBackToEnv(t *testing.T) {
	type cfg struct {
		Password string `env:"TEST_PASSWORD2_EMPTY,required" secret:"true"`
	}
	tmpDir := t.TempDir()
	secretFile := filepath.Join(tmpDir, "empty-password.txt")
	require.NoError(t, os.WriteFile(secretFile, nil, 0600))

	t.Setenv("TEST_PASSWORD2_EMPTY", "direct-value")
	t.Setenv("TEST_PASSWORD2_EMPTY_FILE", secretFile)

	_, err := Load[cfg]()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_PASSWORD2_EMPTY")
	assert.Contains(t, err.Error(), "set but empty")
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
	t.Setenv("TEST_BAD_INT", "secret-token-not-a-number")

	_, err := Load[cfg]()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_BAD_INT")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestLoad_InvalidBool(t *testing.T) {
	type cfg struct {
		Flag bool `env:"TEST_BAD_BOOL"`
	}
	t.Setenv("TEST_BAD_BOOL", "secret-token-maybe")

	_, err := Load[cfg]()
	assert.Error(t, err)
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestLoad_InvalidFloatDoesNotEchoValue(t *testing.T) {
	type cfg struct {
		Rate float64 `env:"TEST_BAD_FLOAT"`
	}
	t.Setenv("TEST_BAD_FLOAT", "secret-token-rate")

	_, err := Load[cfg]()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_BAD_FLOAT")
	assert.NotContains(t, err.Error(), "secret-token")
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
	t.Setenv("TEST_MISSING_SECRET_FILE", "/nonexistent/path/secret-token.txt")

	_, err := Load[cfg]()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_MISSING_SECRET")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "/nonexistent")
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

func TestLoad_URLMissingHostErrorDoesNotEchoValue(t *testing.T) {
	type cfg struct {
		Endpoint *url.URL `env:"TEST_URL_MISSING_HOST"`
	}
	t.Setenv("TEST_URL_MISSING_HOST", "https://?token=secret-token")

	_, err := Load[cfg]()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_URL_MISSING_HOST")
	assert.Contains(t, err.Error(), "scheme and host")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "token=")
}

func TestLoad_URLParseErrorDoesNotEchoValue(t *testing.T) {
	type cfg struct {
		Endpoint *url.URL `env:"TEST_URL_BAD_ESCAPE"`
	}
	t.Setenv("TEST_URL_BAD_ESCAPE", "https://example.com/%zz?token=secret-token")

	_, err := Load[cfg]()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_URL_BAD_ESCAPE")
	assert.Contains(t, err.Error(), "invalid URL syntax")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "token=")
	assert.NotContains(t, err.Error(), "%zz")
}

func TestLoad_SecretTypedParseErrorsDoNotEchoValue(t *testing.T) {
	const secretValue = "secret-token"
	tests := []struct {
		name    string
		envName string
		load    func() error
	}{
		{
			name:    "duration",
			envName: "TEST_SECRET_DURATION",
			load: func() error {
				type cfg struct {
					Timeout time.Duration `env:"TEST_SECRET_DURATION" secret:"true"`
				}
				_, err := Load[cfg]()
				return err
			},
		},
		{
			name:    "int",
			envName: "TEST_SECRET_INT",
			load: func() error {
				type cfg struct {
					Value int `env:"TEST_SECRET_INT" secret:"true"`
				}
				_, err := Load[cfg]()
				return err
			},
		},
		{
			name:    "uint",
			envName: "TEST_SECRET_UINT",
			load: func() error {
				type cfg struct {
					Value uint `env:"TEST_SECRET_UINT" secret:"true"`
				}
				_, err := Load[cfg]()
				return err
			},
		},
		{
			name:    "bool",
			envName: "TEST_SECRET_BOOL",
			load: func() error {
				type cfg struct {
					Enabled bool `env:"TEST_SECRET_BOOL" secret:"true"`
				}
				_, err := Load[cfg]()
				return err
			},
		},
		{
			name:    "float",
			envName: "TEST_SECRET_FLOAT",
			load: func() error {
				type cfg struct {
					Rate float64 `env:"TEST_SECRET_FLOAT" secret:"true"`
				}
				_, err := Load[cfg]()
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.envName, secretValue)

			err := tt.load()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.envName)
			assert.Contains(t, err.Error(), "[REDACTED]")
			assert.NotContains(t, err.Error(), secretValue)
		})
	}
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
	assert.NotContains(t, err.Error(), "noTags")
}

func TestLoad_TypeErrorsDoNotReflectTypeNames(t *testing.T) {
	_, err := Load[*basicConfig]()
	require.Error(t, err)
	assert.EqualError(t, err, "config: Load[T] requires T to be a struct type, not a pointer")
	assert.NotContains(t, err.Error(), "basicConfig")

	_, err = Load[int]()
	require.Error(t, err)
	assert.EqualError(t, err, "config: Load[T] requires T to be a struct type")
	assert.NotContains(t, err.Error(), "int")
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
