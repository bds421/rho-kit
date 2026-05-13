package redis

import (
	"crypto/tls"
	"log/slog"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ slog.LogValuer = Config{}

func TestRedisConfig_LogValue_Redacts(t *testing.T) {
	cfg := Config{URL: "redis://token-user:secret@tenant-redis.internal:6379/0?token=query-secret#frag"}
	val := cfg.LogValue()
	s := val.String()
	assert.NotContains(t, s, "token-user")
	assert.NotContains(t, s, "secret")
	assert.NotContains(t, s, "query-secret")
	assert.NotContains(t, s, "tenant-redis.internal")
	assert.Contains(t, s, "url_configured=true")
	assert.Contains(t, s, "url_valid=true")
	assert.Contains(t, s, "host_configured=true")
	assert.Contains(t, s, "password_configured=true")
}

func TestRedisConfig_LogValue_InvalidURL(t *testing.T) {
	cfg := Config{URL: "://invalid"}
	val := cfg.LogValue()
	assert.Contains(t, val.String(), "url_valid=false")
}

func TestRedisConfig_LogValue_NotConfigured(t *testing.T) {
	cfg := Config{}
	assert.Contains(t, cfg.LogValue().String(), "url_configured=false")
}

func TestRedisConfig_RedisURL_FromURL(t *testing.T) {
	cfg := Config{URL: "redis://user:pass@host:6379/0"}
	assert.Equal(t, "redis://user:pass@host:6379/0", cfg.RedisURL())
}

func TestRedisConfig_RedisURL_URLTakesPrecedence(t *testing.T) {
	cfg := Config{
		URL:  "redis://:pass@url-host:6379/0",
		Host: "field-host",
	}
	assert.Equal(t, "redis://:pass@url-host:6379/0", cfg.RedisURL())
}

func TestRedisConfig_RedisURL_FromFields(t *testing.T) {
	cfg := Config{Host: "myredis", Port: 6380, Password: "secret", DB: 2}
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
	cfg := Config{Host: "redis"}
	parsed, err := url.Parse(cfg.RedisURL())
	require.NoError(t, err)
	assert.Equal(t, "6379", parsed.Port())
}

func TestRedisConfig_RedisURL_EmptyHost(t *testing.T) {
	cfg := Config{}
	assert.Empty(t, cfg.RedisURL())
}

func TestRedisConfig_RedisURL_NoPassword(t *testing.T) {
	cfg := Config{Host: "redis"}
	u := cfg.RedisURL()
	parsed, err := url.Parse(u)
	require.NoError(t, err)
	assert.Nil(t, parsed.User)
}

func TestRedisConfig_Options_FromURL(t *testing.T) {
	cfg := Config{URL: "redis://:secret@localhost:6379/2"}
	opts, err := cfg.Options()
	require.NoError(t, err)
	assert.Equal(t, "localhost:6379", opts.Addr)
	assert.Equal(t, "secret", opts.Password)
	assert.Equal(t, 2, opts.DB)
}

func TestRedisConfig_Options_RedissEnforcesTLSFloor(t *testing.T) {
	cfg := Config{URL: "rediss://:secret@localhost:6380/2"}
	opts, err := cfg.Options()
	require.NoError(t, err)
	require.NotNil(t, opts.TLSConfig)
	assert.Equal(t, uint16(minimumTLSVersion), opts.TLSConfig.MinVersion)
	assert.Equal(t, "localhost", opts.TLSConfig.ServerName)
}

func TestRedisConfig_Options_RejectsSkipVerify(t *testing.T) {
	cfg := Config{URL: "rediss://:secret@localhost:6380/2?skip_verify=true"}
	_, err := cfg.Options()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "skip_verify")
}

func TestCloneTLSConfigWithFloor_ClonesAndEnforcesFloor(t *testing.T) {
	cfg := &tls.Config{ServerName: "redis.internal.test"}
	cfg.MinVersion = minimumTLSVersion - 1

	cloned, err := cloneTLSConfigWithFloor(cfg)
	require.NoError(t, err)
	require.NotNil(t, cloned)
	assert.NotSame(t, cfg, cloned)
	assert.Equal(t, uint16(minimumTLSVersion-1), cfg.MinVersion)
	assert.Equal(t, uint16(minimumTLSVersion), cloned.MinVersion)
	assert.Equal(t, "redis.internal.test", cloned.ServerName)
}

func TestCloneTLSConfigWithFloor_RejectsMaxVersionBelowFloor(t *testing.T) {
	_, err := cloneTLSConfigWithFloor(&tls.Config{MaxVersion: minimumTLSVersion - 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS MaxVersion")
}

func TestRedisConfig_Options_FromFields(t *testing.T) {
	cfg := Config{Host: "myredis", Port: 6380, Password: "pass", DB: 3}
	opts, err := cfg.Options()
	require.NoError(t, err)
	assert.Equal(t, "myredis:6380", opts.Addr)
	assert.Equal(t, "pass", opts.Password)
	assert.Equal(t, 3, opts.DB)
}

func TestRedisConfig_Options_NotConfigured(t *testing.T) {
	cfg := Config{}
	_, err := cfg.Options()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "neither URL nor Host")
}

func TestLoadRedisFields_URL(t *testing.T) {
	t.Setenv("REDIS_URL", "redis://:pass@redis-host:6379/0")

	f, err := LoadFields()
	require.NoError(t, err)
	assert.Equal(t, "redis://:pass@redis-host:6379/0", f.Redis.RedisURL())
}

func TestLoadRedisFields_URLReadsAllowPlaintext(t *testing.T) {
	t.Setenv("REDIS_URL", "redis://:pass@redis-host:6379/0")
	t.Setenv("REDIS_ALLOW_PLAINTEXT", "true")

	f, err := LoadFields()
	require.NoError(t, err)
	assert.True(t, f.Redis.AllowPlaintext)
}

func TestLoadRedisFields_IndividualFields(t *testing.T) {
	t.Setenv("REDIS_HOST", "my-redis")
	t.Setenv("REDIS_PORT", "6380")
	t.Setenv("REDIS_PASSWORD", "strongpass")
	t.Setenv("REDIS_DB", "3")
	t.Setenv("REDIS_ALLOW_PLAINTEXT", "true")

	f, err := LoadFields()
	require.NoError(t, err)
	assert.Equal(t, "my-redis", f.Redis.Host)
	assert.Equal(t, 6380, f.Redis.Port)
	assert.Equal(t, "strongpass", f.Redis.Password)
	assert.Equal(t, 3, f.Redis.DB)
	assert.True(t, f.Redis.AllowPlaintext)
	assert.Contains(t, f.Redis.RedisURL(), "my-redis:6380")
}

func TestLoadRedisFields_URLPrecedence(t *testing.T) {
	t.Setenv("REDIS_URL", "redis://:urlpass@url-host:6379/0")
	t.Setenv("REDIS_HOST", "field-host")
	t.Setenv("REDIS_PASSWORD", "field-pass")

	f, err := LoadFields()
	require.NoError(t, err)
	assert.Equal(t, "redis://:urlpass@url-host:6379/0", f.Redis.RedisURL())
}

func TestLoadRedisFields_Defaults(t *testing.T) {
	t.Setenv("REDIS_HOST", "redis")

	f, err := LoadFields()
	require.NoError(t, err)
	assert.Equal(t, 6379, f.Redis.Port)
	assert.Equal(t, 0, f.Redis.DB)
	assert.Empty(t, f.Redis.Password)
}

func TestLoadRedisFields_InvalidAllowPlaintext(t *testing.T) {
	t.Setenv("REDIS_URL", "redis://:pass@redis-host:6379/0")
	t.Setenv("REDIS_ALLOW_PLAINTEXT", "not-bool")

	_, err := LoadFields()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "REDIS_ALLOW_PLAINTEXT")
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
		{"missing host", "rediss:///0", true},
		{"empty hostname", "rediss://:6380/0", true},
		{"empty port", "rediss://host:/0", true},
		{"zero port", "rediss://host:0/0", true},
		{"too large port", "rediss://host:65536/0", true},
		{"zone identifier", "rediss://[fe80::1%25lo0]:6380/0", true},
		{"skip verify", "rediss://host:6380/0?skip_verify=true", true},
		{"skip verify false still rejected", "rediss://host:6380/0?skip_verify=false", true},
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

func TestValidateRedisURL_ParseErrorDoesNotEchoValue(t *testing.T) {
	err := ValidateRedisURL("REDIS_URL", "rediss://redis.example.com/%zz?token=secret-token")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "REDIS_URL is invalid")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "token=")
	assert.NotContains(t, err.Error(), "%zz")
}

func TestValidateRedisURL_UnsupportedSchemeDoesNotEchoValue(t *testing.T) {
	err := ValidateRedisURL("REDIS_URL", "secret-token-scheme://redis.example.com/0")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scheme must be redis or rediss")
	assert.NotContains(t, err.Error(), "secret-token-scheme")
}

func TestRedisFields_ValidateRedis_RejectsCredentiallessRedissWithoutPanic(t *testing.T) {
	fields := Fields{Redis: Config{URL: "rediss://localhost:6380/0"}}

	require.NotPanics(t, func() {
		err := fields.ValidateRedis("")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no credentials")
	})
}

func TestRedisFields_ValidateRedis_AllowsRedissWithPasswordOnlyURL(t *testing.T) {
	fields := Fields{Redis: Config{URL: "rediss://:secret@localhost:6380/0"}}

	require.NoError(t, fields.ValidateRedis(""))
}
