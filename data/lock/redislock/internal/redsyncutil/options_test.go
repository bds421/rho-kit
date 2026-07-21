package redsyncutil_test

import (
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/lock/redislock/v2/internal/redsyncutil"
)

func TestDefaultConfig(t *testing.T) {
	cfg := redsyncutil.DefaultConfig()
	assert.Equal(t, 30*time.Second, cfg.TTL)
	assert.Equal(t, "lock:", cfg.Prefix)
	assert.Nil(t, cfg.Logger)
	assert.Zero(t, cfg.MaxWait)
	assert.Zero(t, cfg.MaxAttempts)
}

func TestApply_DefaultsLoggerAndRejectsNil(t *testing.T) {
	cfg := redsyncutil.DefaultConfig()
	redsyncutil.Apply("redislock", &cfg)
	require.NotNil(t, cfg.Logger)

	assert.PanicsWithValue(t, "redislock: option must not be nil", func() {
		c := redsyncutil.DefaultConfig()
		redsyncutil.Apply("redislock", &c, nil)
	})
}

func TestWithTTL_AndFriends(t *testing.T) {
	assert.Panics(t, func() { redsyncutil.WithTTL("redislock", 0) })
	assert.Panics(t, func() { redsyncutil.WithRetry("redlock", 0, 1) })
	assert.Panics(t, func() { redsyncutil.WithRetry("redlock", time.Millisecond, -1) })
	assert.Panics(t, func() { redsyncutil.WithMaxWait("redislock", 0) })

	custom := slog.Default()
	cfg := redsyncutil.DefaultConfig()
	redsyncutil.Apply("redislock", &cfg,
		redsyncutil.WithTTL("redislock", 5*time.Second),
		redsyncutil.WithRetry("redislock", 10*time.Millisecond, 3),
		redsyncutil.WithMaxWait("redislock", time.Second),
		redsyncutil.WithLogger("redislock", custom),
		redsyncutil.WithKeyPrefix("redislock", "my:"),
	)
	assert.Equal(t, 5*time.Second, cfg.TTL)
	assert.Equal(t, 10*time.Millisecond, cfg.RetryInterval)
	assert.Equal(t, 3, cfg.MaxAttempts)
	assert.Equal(t, time.Second, cfg.MaxWait)
	assert.Equal(t, custom, cfg.Logger)
	assert.Equal(t, "my:", cfg.Prefix)

	// nil logger must not clobber an existing one.
	redsyncutil.WithLogger("redislock", nil)(&cfg)
	assert.Equal(t, custom, cfg.Logger)
}
