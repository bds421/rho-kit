package redsyncutil_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-redsync/redsync/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/data/lock/redislock/v2/internal/redsyncutil"
	"github.com/bds421/rho-kit/data/v2/lock"
)

func TestTryCount(t *testing.T) {
	assert.Equal(t, 1, redsyncutil.TryCount(0))
	assert.Equal(t, 1, redsyncutil.TryCount(-1))
	assert.Equal(t, 3, redsyncutil.TryCount(3))
}

func TestValidateLockKey(t *testing.T) {
	require.NoError(t, redsyncutil.ValidateLockKey("redislock", "ok-key"))
	assert.Error(t, redsyncutil.ValidateLockKey("redislock", ""))
	assert.Error(t, redsyncutil.ValidateLockKey("redislock", "bad"+string(rune(0))+"key"))
	assert.Contains(t, redsyncutil.ValidateLockKey("redlock", "").Error(), "redlock:")
}

func TestIsContentionAndLost(t *testing.T) {
	assert.True(t, redsyncutil.IsContentionError(redsync.ErrFailed))
	assert.True(t, redsyncutil.IsLockLostError(redsync.ErrLockAlreadyExpired))
	assert.False(t, redsyncutil.IsContentionError(errors.New("other")))
}

func TestDetachedReleaseContext(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	ctx, stop := redsyncutil.DetachedReleaseContext(parent, time.Second)
	defer stop()
	assert.NoError(t, ctx.Err(), "must not inherit parent cancel")
	assert.ErrorIs(t, parent.Err(), context.Canceled)
}

func TestHandle_Construction(t *testing.T) {
	h := redsyncutil.NewHandle(nil, "lock")
	assert.NotNil(t, h)
	_ = lock.ErrLockLost
}
