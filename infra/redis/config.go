package redis

import (
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strconv"

	goredis "github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/config"
	"github.com/bds421/rho-kit/core/v2/tlsclone"
)

const minimumTLSVersion = tls.VersionTLS12

// RedisConfig holds Redis connection settings.
//
// Configure via URL directly, or via individual fields (Host, Port, Password, DB)
// which are assembled into a Redis URL. When URL is non-empty it takes
// precedence over individual fields.
type RedisConfig struct {
	URL      string
	Host     string
	Port     int
	Password string
	DB       int
	// AllowPlaintext opts a deployment out of the FR-077 production-
	// safety check. Without it, ValidateRedis rejects `redis://` URLs
	// and credential-less connections. Set REDIS_ALLOW_PLAINTEXT=true
	// only for genuinely trusted local-dev fixtures.
	AllowPlaintext bool `env:"REDIS_ALLOW_PLAINTEXT"`
}

// RedisURL returns the resolved Redis connection URL. If URL is set directly,
// it is returned as-is. Otherwise, the URL is built from individual fields.
// Returns an empty string if neither URL nor Host is configured.
func (c RedisConfig) RedisURL() string {
	if c.URL != "" {
		return c.URL
	}
	if c.Host == "" {
		return ""
	}
	port := c.Port
	if port == 0 {
		port = 6379
	}
	u := &url.URL{
		Scheme: "redis",
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(port)),
		Path:   strconv.Itoa(c.DB),
	}
	if c.Password != "" {
		u.User = url.UserPassword("", c.Password)
	}
	return u.String()
}

// Options returns go-redis Options parsed from the resolved URL or built from
// individual fields. This is the primary way to convert a RedisConfig into
// the *redis.Options that Connect() and the Builder expect.
func (c RedisConfig) Options() (*goredis.Options, error) {
	resolved := c.RedisURL()
	if resolved == "" {
		return nil, fmt.Errorf("redis: neither URL nor Host is configured")
	}
	if err := ValidateRedisURL("REDIS_URL", resolved); err != nil {
		return nil, err
	}
	opts, err := goredis.ParseURL(resolved)
	if err != nil {
		return nil, fmt.Errorf("redis: URL is invalid")
	}
	if opts.TLSConfig != nil {
		opts.TLSConfig, err = cloneTLSConfigWithFloor(opts.TLSConfig)
		if err != nil {
			return nil, err
		}
	}
	return opts, nil
}

func cloneTLSConfigWithFloor(cfg *tls.Config) (*tls.Config, error) {
	cloned, err := tlsclone.ConfigWithFloor(cfg, minimumTLSVersion)
	if err != nil {
		return nil, fmt.Errorf("redis: TLS MaxVersion must allow TLS 1.2 or newer")
	}
	return cloned, nil
}

// LogValue implements slog.LogValuer to prevent accidental logging of credentials
// or topology embedded in the Redis URL.
func (c RedisConfig) LogValue() slog.Value {
	urlValid, urlHostConfigured, urlUserinfoConfigured := redisURLLogState(c.URL)
	return slog.GroupValue(
		slog.Bool("url_configured", c.URL != ""),
		slog.Bool("url_valid", urlValid),
		slog.Bool("host_configured", c.Host != "" || urlHostConfigured),
		slog.Int("port", c.Port),
		slog.Bool("password_configured", c.Password != "" || urlUserinfoConfigured),
		slog.Int("db", c.DB),
		slog.Bool("allow_plaintext", c.AllowPlaintext),
	)
}

func redisURLLogState(rawURL string) (valid, hostConfigured, userinfoConfigured bool) {
	if rawURL == "" {
		return true, false, false
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return false, false, false
	}
	return true, u.Host != "", u.User != nil
}

// RedisFields holds Redis connection configuration.
// Embed this in service configs that use Redis.
type RedisFields struct {
	Redis RedisConfig
}

// LoadRedisFields reads the Redis connection config from environment variables.
//
// If REDIS_URL is set, it is used directly. Otherwise, the connection is
// built from individual fields:
//   - REDIS_HOST (required when no URL)
//   - REDIS_PORT (default: 6379)
//   - REDIS_PASSWORD (secret, default: empty)
//   - REDIS_DB (default: 0)
func LoadRedisFields() (RedisFields, error) {
	allowPlaintext, err := config.GetBool("REDIS_ALLOW_PLAINTEXT", false)
	if err != nil {
		return RedisFields{}, err
	}

	// REDIS_URL takes precedence.
	if rawURL := config.MustGetSecret("REDIS_URL", ""); rawURL != "" {
		return RedisFields{
			Redis: RedisConfig{URL: rawURL, AllowPlaintext: allowPlaintext},
		}, nil
	}

	// Fallback: individual env vars.
	p := &config.Parser{}
	port := p.Int("REDIS_PORT", 6379)
	db := p.Int("REDIS_DB", 0)
	if err := p.Err(); err != nil {
		return RedisFields{}, err
	}

	return RedisFields{
		Redis: RedisConfig{
			Host:           config.Get("REDIS_HOST", ""),
			Port:           port,
			Password:       config.MustGetSecret("REDIS_PASSWORD", ""),
			DB:             db,
			AllowPlaintext: allowPlaintext,
		},
	}, nil
}

// ValidateRedis checks the Redis configuration and credential strength.
//
// The environment parameter is preserved for backward compatibility but
// no longer gates any check — the kit no longer has a development mode
// (see docs/RELEASE_NOTES_v2.md). Production-safe defaults are
// unconditional. Tests against fixture instances should provide
// password-bearing URLs via REDIS_URL or set REDIS_PASSWORD.
//
// FR-077 [MED]: rejects passwordless URLs and the plaintext `redis://`
// scheme unless [Redis.AllowPlaintext] is set. Production deployments
// must use `rediss://` and supply a credential; the explicit opt-out
// keeps local-dev fixtures working while making the unsafe path
// loud.
func (f RedisFields) ValidateRedis(environment string) error {
	_ = environment // accepted for API compatibility; not consulted
	resolved := f.Redis.RedisURL()
	if err := ValidateRedisURL("REDIS_URL", resolved); err != nil {
		return err
	}
	if !f.Redis.AllowPlaintext {
		if u, err := url.Parse(resolved); err == nil {
			if u.Scheme == "redis" {
				return fmt.Errorf("REDIS_URL uses plaintext scheme \"redis://\"; set REDIS_ALLOW_PLAINTEXT=true to permit (FR-077)")
			}
			if !redisURLHasCredentials(u) && f.Redis.Password == "" {
				return fmt.Errorf("REDIS_URL has no credentials and REDIS_PASSWORD is empty; set REDIS_ALLOW_PLAINTEXT=true to permit anonymous Redis (FR-077)")
			}
		}
	}
	if f.Redis.Password == "" && f.Redis.URL == "" {
		return fmt.Errorf("REDIS_PASSWORD is required (or pass it via REDIS_URL)")
	}
	return nil
}

func redisURLHasCredentials(u *url.URL) bool {
	if u == nil || u.User == nil {
		return false
	}
	if u.User.Username() != "" {
		return true
	}
	pw, hasPassword := u.User.Password()
	return hasPassword && pw != ""
}

// ValidateRedisURL checks that rawURL is a non-empty, parseable URL with a
// redis or rediss scheme. name is used in error messages (e.g. "REDIS_URL").
func ValidateRedisURL(name, rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("%s is required", name)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%s is invalid", name)
	}
	if u.Scheme != "redis" && u.Scheme != "rediss" {
		return fmt.Errorf("%s scheme must be redis or rediss", name)
	}
	if err := config.ValidateURLHost(name, u); err != nil {
		return err
	}
	if _, ok := u.Query()["skip_verify"]; ok {
		return fmt.Errorf("%s must not set skip_verify; Redis TLS verification cannot be disabled", name)
	}
	return nil
}
