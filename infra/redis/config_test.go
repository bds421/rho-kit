package redis

import (
	"log/slog"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ slog.LogValuer = RedisConfig{}

func TestRedisConfig_LogValue_Redacts(t *testing.T) {
	cfg := RedisConfig{URL: "redis://user:secret@localhost:6379/0"}
	val := cfg.LogValue()
	s := val.String()
	assert.NotContains(t, s, "secret")
	assert.Contains(t, s, "localhost")
}

func TestRedisConfig_LogValue_InvalidURL(t *testing.T) {
	cfg := RedisConfig{URL: "://invalid"}
	val := cfg.LogValue()
	assert.Equal(t, "[INVALID URL]", val.String())
}

func TestRedisConfig_LogValue_NotConfigured(t *testing.T) {
	cfg := RedisConfig{}
	assert.Equal(t, "[NOT CONFIGURED]", cfg.LogValue().String())
}

func TestRedisConfig_RedisURL_FromURL(t *testing.T) {
	cfg := RedisConfig{URL: "redis://user:pass@host:6379/0"}
	assert.Equal(t, "redis://user:pass@host:6379/0", cfg.RedisURL())
}

func TestRedisConfig_RedisURL_URLTakesPrecedence(t *testing.T) {
	cfg := RedisConfig{
		URL:  "redis://:pass@url-host:6379/0",
		Host: "field-host",
	}
	assert.Equal(t, "redis://:pass@url-host:6379/0", cfg.RedisURL())
}

func TestRedisConfig_RedisURL_FromFields(t *testing.T) {
	cfg := RedisConfig{Host: "myredis", Port: 6380, Password: "secret", DB: 2}
	u := cfg.RedisURL()
	parsed, err := url.Parse(u)
	require.NoError(t, err)
	assert.Equal(t, "redis", parsed.Scheme)
	assert.Equal(t, "myredis", parsed.Hostname())
	assert.Equal(t, "6380", parsed.Port())
	pw, _ := parsed.User.Password()
	assert.Equal(t, "secret", pw)
	assert.Equal(t, "/2", parsed.Path)
}

func TestRedisConfig_RedisURL_DefaultPort(t *testing.T) {
	cfg := RedisConfig{Host: "redis"}
	parsed, err := url.Parse(cfg.RedisURL())
	require.NoError(t, err)
	assert.Equal(t, "6379", parsed.Port())
}

func TestRedisConfig_RedisURL_EmptyHost(t *testing.T) {
	cfg := RedisConfig{}
	assert.Empty(t, cfg.RedisURL())
}

func TestRedisConfig_RedisURL_NoPassword(t *testing.T) {
	cfg := RedisConfig{Host: "redis"}
	u := cfg.RedisURL()
	parsed, err := url.Parse(u)
	require.NoError(t, err)
	assert.Nil(t, parsed.User)
}

func TestRedisConfig_Options_FromURL(t *testing.T) {
	cfg := RedisConfig{URL: "redis://:secret@localhost:6379/2"}
	opts, err := cfg.Options()
	require.NoError(t, err)
	assert.Equal(t, "localhost:6379", opts.Addr)
	assert.Equal(t, "secret", opts.Password)
	assert.Equal(t, 2, opts.DB)
}

func TestRedisConfig_Options_FromFields(t *testing.T) {
	cfg := RedisConfig{Host: "myredis", Port: 6380, Password: "pass", DB: 3}
	opts, err := cfg.Options()
	require.NoError(t, err)
	assert.Equal(t, "myredis:6380", opts.Addr)
	assert.Equal(t, "pass", opts.Password)
	assert.Equal(t, 3, opts.DB)
}

func TestRedisConfig_Options_NotConfigured(t *testing.T) {
	cfg := RedisConfig{}
	_, err := cfg.Options()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither URL nor Host")
}

func TestLoadRedisFields_URL(t *testing.T) {
	t.Setenv("REDIS_URL", "redis://:pass@redis-host:6379/0")

	f, err := LoadRedisFields()
	require.NoError(t, err)
	assert.Equal(t, "redis://:pass@redis-host:6379/0", f.Redis.RedisURL())
}

func TestLoadRedisFields_IndividualFields(t *testing.T) {
	t.Setenv("REDIS_HOST", "my-redis")
	t.Setenv("REDIS_PORT", "6380")
	t.Setenv("REDIS_PASSWORD", "strongpass")
	t.Setenv("REDIS_DB", "3")

	f, err := LoadRedisFields()
	require.NoError(t, err)
	assert.Equal(t, "my-redis", f.Redis.Host)
	assert.Equal(t, 6380, f.Redis.Port)
	assert.Equal(t, "strongpass", f.Redis.Password)
	assert.Equal(t, 3, f.Redis.DB)
	assert.Contains(t, f.Redis.RedisURL(), "my-redis:6380")
}

func TestLoadRedisFields_URLPrecedence(t *testing.T) {
	t.Setenv("REDIS_URL", "redis://:urlpass@url-host:6379/0")
	t.Setenv("REDIS_HOST", "field-host")
	t.Setenv("REDIS_PASSWORD", "field-pass")

	f, err := LoadRedisFields()
	require.NoError(t, err)
	assert.Equal(t, "redis://:urlpass@url-host:6379/0", f.Redis.RedisURL())
}

func TestLoadRedisFields_Defaults(t *testing.T) {
	t.Setenv("REDIS_HOST", "redis")

	f, err := LoadRedisFields()
	require.NoError(t, err)
	assert.Equal(t, 6379, f.Redis.Port)
	assert.Equal(t, 0, f.Redis.DB)
	assert.Empty(t, f.Redis.Password)
}

func TestValidateRedisURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"valid redis", "redis://localhost:6379/0", false},
		{"valid rediss (TLS)", "rediss://user:pass@host:6380/0", false},
		{"empty", "", true},
		{"wrong scheme http", "http://localhost:6379/", true},
		{"wrong scheme amqp", "amqp://localhost:5672/", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateRedisURL("REDIS_URL", tt.url)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
