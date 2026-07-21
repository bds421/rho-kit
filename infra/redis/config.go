package redis

import (
	"crypto/tls"
	"errors"
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

// Config holds Redis connection settings.
//
// Configure via URL directly, or via individual fields (Host, Port,
// Password, DB, TLS) which are assembled into a Redis URL. When URL is
// non-empty it takes precedence over individual fields.
//
// Production operators using the individual-fields path MUST set
// [Config.TLS] (REDIS_TLS=true) so RedisURL() emits rediss://. Without
// TLS the fields path is plaintext-only and requires
// REDIS_ALLOW_PLAINTEXT=true, which also disables the passwordless
// guard — suitable only for trusted local-dev fixtures.
type Config struct {
	URL      string
	Host     string
	Port     int
	Password string
	DB       int
	// TLS, when true, makes RedisURL() assemble a rediss:// URL from the
	// individual fields. Ignored when URL is set. Env: REDIS_TLS.
	TLS bool `env:"REDIS_TLS"`
	// AllowPlaintext opts a deployment out of the FR-077 production-
	// safety check. Without it, ValidateRedis rejects `redis://` URLs
	// and credential-less connections. Set REDIS_ALLOW_PLAINTEXT=true
	// only for genuinely trusted local-dev fixtures.
	AllowPlaintext bool `env:"REDIS_ALLOW_PLAINTEXT"`
}

// RedisURL returns the resolved Redis connection URL. If URL is set directly,
// it is returned as-is. Otherwise, the URL is built from individual fields.
// Returns an empty string if neither URL nor Host is configured.
func (c Config) RedisURL() string {
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
	scheme := "redis"
	if c.TLS {
		scheme = "rediss"
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   net.JoinHostPort(c.Host, strconv.Itoa(port)),
		Path:   strconv.Itoa(c.DB),
	}
	if c.Password != "" {
		u.User = url.UserPassword("", c.Password)
	}
	return u.String()
}

// Options returns go-redis Options parsed from the resolved URL or built from
// individual fields. This is the primary way to convert a Config into
// the *redis.Options that Connect() and the Builder expect.
//
// FR-077 is enforced here so callers using Fields.Redis.Options() +
// Connect cannot bypass the plaintext/passwordless guard that
// [Fields.ValidateRedis] applies during preflight. Wave 66 closed a
// hostile-review finding that the two paths disagreed.
func (c Config) Options() (*goredis.Options, error) {
	resolved := c.RedisURL()
	if resolved == "" {
		return nil, fmt.Errorf("redis: neither URL nor Host is configured")
	}
	if err := ValidateRedisURL("REDIS_URL", resolved); err != nil {
		return nil, err
	}
	if err := c.checkFR077(resolved); err != nil {
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

// checkFR077 enforces the plaintext / passwordless gate on a Config
// independently of Fields.ValidateRedis. Kept private so the preflight
// path and the runtime Options path stay in sync.
func (c Config) checkFR077(resolved string) error {
	if c.AllowPlaintext {
		return nil
	}
	u, err := url.Parse(resolved)
	if err != nil {
		return nil // ValidateRedisURL already surfaces this
	}
	if u.Scheme == "redis" {
		return fmt.Errorf("REDIS_URL uses plaintext scheme \"redis://\"; set REDIS_ALLOW_PLAINTEXT=true to permit (FR-077)")
	}
	// When URL is set, RedisURL() returns it verbatim and Options() builds
	// the dialer solely from ParseURL(URL) — the Config.Password field is
	// silently ignored. Only URL userinfo can carry a credential in that
	// case, so counting c.Password here would let a credentialless URL
	// connect anonymously while passing the FR-077 guard. When URL is empty,
	// RedisURL() embeds c.Password into the assembled URL, so the parsed
	// userinfo already reflects it.
	if !redisURLHasCredentials(u) {
		return fmt.Errorf("REDIS_URL has no credentials and REDIS_PASSWORD is empty; set REDIS_ALLOW_PLAINTEXT=true to permit anonymous Redis (FR-077)")
	}
	return nil
}

func cloneTLSConfigWithFloor(cfg *tls.Config) (*tls.Config, error) {
	cloned, err := tlsclone.ConfigWithFloor(cfg, minimumTLSVersion)
	if err != nil {
		if errors.Is(err, tlsclone.ErrInsecureSkipVerifyNotPermitted) {
			return nil, fmt.Errorf("redis: TLS InsecureSkipVerify=true is not permitted")
		}
		return nil, fmt.Errorf("redis: TLS MaxVersion must allow TLS 1.2 or newer")
	}
	return cloned, nil
}

// LogValue implements slog.LogValuer to prevent accidental logging of credentials
// or topology embedded in the Redis URL.
func (c Config) LogValue() slog.Value {
	urlValid, urlHostConfigured, urlUserinfoConfigured := redisURLLogState(c.URL)
	return slog.GroupValue(
		slog.Bool("url_configured", c.URL != ""),
		slog.Bool("url_valid", urlValid),
		slog.Bool("host_configured", c.Host != "" || urlHostConfigured),
		slog.Int("port", c.Port),
		slog.Bool("password_configured", c.Password != "" || urlUserinfoConfigured),
		slog.Int("db", c.DB),
		slog.Bool("tls", c.TLS),
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

// Fields holds Redis connection configuration.
// Embed this in service configs that use Redis.
type Fields struct {
	Redis Config
}

// LoadFields reads the Redis connection config from environment variables.
//
// If REDIS_URL is set, it is used directly. Otherwise, the connection is
// built from individual fields:
//   - REDIS_HOST (required when no URL)
//   - REDIS_PORT (default: 6379)
//   - REDIS_PASSWORD (secret, default: empty)
//   - REDIS_DB (default: 0)
//   - REDIS_TLS (default: false; set true for rediss:// from fields)
//
// The fields path without REDIS_TLS=true is plaintext-only. Production
// deployments should set REDIS_TLS=true (or use REDIS_URL=rediss://...).
func LoadFields() (Fields, error) {
	allowPlaintext, err := config.GetBool("REDIS_ALLOW_PLAINTEXT", false)
	if err != nil {
		return Fields{}, err
	}
	tlsEnabled, err := config.GetBool("REDIS_TLS", false)
	if err != nil {
		return Fields{}, err
	}

	// REDIS_URL takes precedence.
	if rawURL := config.MustGetSecret("REDIS_URL", ""); rawURL != "" {
		return Fields{
			Redis: Config{URL: rawURL, AllowPlaintext: allowPlaintext},
		}, nil
	}

	// Fallback: individual env vars.
	p := &config.Parser{}
	port := p.Int("REDIS_PORT", 6379)
	db := p.Int("REDIS_DB", 0)
	if err := p.Err(); err != nil {
		return Fields{}, err
	}

	return Fields{
		Redis: Config{
			Host:           config.Get("REDIS_HOST", ""),
			Port:           port,
			Password:       config.MustGetSecret("REDIS_PASSWORD", ""),
			DB:             db,
			TLS:            tlsEnabled,
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
func (f Fields) ValidateRedis(environment string) error {
	_ = environment // accepted for API compatibility; not consulted
	resolved := f.Redis.RedisURL()
	if err := ValidateRedisURL("REDIS_URL", resolved); err != nil {
		return err
	}
	if err := f.Redis.checkFR077(resolved); err != nil {
		return err
	}
	// AllowPlaintext is the explicit opt-out for trusted local-dev fixtures;
	// checkFR077 already honored it above. Enforcing a password here too would
	// make the fields path (REDIS_HOST without a password) reject configs that
	// Config.Options() accepts, so the two paths would disagree. Skip the
	// password requirement when the opt-out is set so both paths stay in sync.
	if !f.Redis.AllowPlaintext && f.Redis.Password == "" && f.Redis.URL == "" {
		return fmt.Errorf("REDIS_PASSWORD is required (or pass it via REDIS_URL)")
	}
	return nil
}

func redisURLHasCredentials(u *url.URL) bool {
	if u == nil || u.User == nil {
		return false
	}
	// FR-077 guards against anonymous Redis. A username alone (no
	// password) is not a secret — Redis ACL nopass users would pass a
	// username-only check while remaining effectively unauthenticated.
	// Require a non-empty password component.
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
