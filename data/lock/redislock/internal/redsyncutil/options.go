package redsyncutil

import (
	"log/slog"
	"time"
)

// Config holds the shared locker option surface used by both redislock
// and redlock. Keeping a single struct prevents the two packages from
// drifting on TTL/retry/max-wait/prefix defaults (review residual polish).
type Config struct {
	TTL           time.Duration
	RetryInterval time.Duration
	MaxAttempts   int
	MaxWait       time.Duration
	Logger        *slog.Logger
	Prefix        string
}

// DefaultConfig returns the kit defaults: 30s TTL and "lock:" key prefix.
// Retry, max-wait, and logger stay zero/nil until Apply or With* set them.
func DefaultConfig() Config {
	return Config{
		TTL:    30 * time.Second,
		Prefix: "lock:",
	}
}

// Option mutates a [Config]. Both redislock and redlock re-export this
// type so a single option surface is shared without an exported
// dependency on this internal package from outside redislock/.
type Option func(*Config)

// Apply applies opts to cfg. Nil options panic with
// "<label>: option must not be nil". When Logger is still nil after
// options, it is set to [slog.Default].
func Apply(label string, cfg *Config, opts ...Option) {
	for _, fn := range opts {
		if fn == nil {
			panic(label + ": option must not be nil")
		}
		fn(cfg)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
}

// WithTTL returns an Option that sets lock expiration. d must be positive;
// otherwise the constructor panics with "<label>: WithTTL requires a positive duration".
func WithTTL(label string, d time.Duration) Option {
	if d <= 0 {
		panic(label + ": WithTTL requires a positive duration")
	}
	return func(c *Config) { c.TTL = d }
}

// WithRetry returns an Option that configures contention retry.
// maxAttempts is the total number of acquisition attempts; zero selects
// one attempt and negative values panic, matching both public packages.
func WithRetry(label string, interval time.Duration, maxAttempts int) Option {
	if interval <= 0 {
		panic(label + ": WithRetry requires a positive interval")
	}
	if maxAttempts < 0 {
		panic(label + ": WithRetry requires maxAttempts >= 0")
	}
	return func(c *Config) {
		c.RetryInterval = interval
		c.MaxAttempts = maxAttempts
	}
}

// WithMaxWait returns an Option that caps Acquire wall-clock retry time.
func WithMaxWait(label string, d time.Duration) Option {
	if d <= 0 {
		panic(label + ": WithMaxWait requires a positive duration")
	}
	return func(c *Config) { c.MaxWait = d }
}

// WithLogger returns an Option that sets the release-failure logger.
// A nil logger is ignored so a later default fallback can apply.
func WithLogger(_ string, l *slog.Logger) Option {
	return func(c *Config) {
		if l != nil {
			c.Logger = l
		}
	}
}

// WithKeyPrefix returns an Option that sets the Redis key namespace.
// Pass "" for deliberate flat-key migration of existing deployments.
func WithKeyPrefix(_ string, p string) Option {
	return func(c *Config) { c.Prefix = p }
}
