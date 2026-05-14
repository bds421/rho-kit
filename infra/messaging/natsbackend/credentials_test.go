package natsbackend

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserPassBridge_AppliesTimeout(t *testing.T) {
	started := make(chan struct{})
	var seen time.Duration
	bridge := newUserPassBridge(func(ctx context.Context) (string, string, error) {
		close(started)
		if dl, ok := ctx.Deadline(); ok {
			seen = time.Until(dl)
		}
		return "u", "p", nil
	}, 250*time.Millisecond)

	u, p := bridge()
	<-started
	assert.Equal(t, "u", u)
	assert.Equal(t, "p", p)
	assert.Greater(t, seen, time.Duration(0), "ctx must carry a deadline")
	assert.LessOrEqual(t, seen, 250*time.Millisecond, "deadline must respect configured timeout")
}

func TestUserPassBridge_FallsBackToCacheOnError(t *testing.T) {
	var calls atomic.Int64
	bridge := newUserPassBridge(func(context.Context) (string, string, error) {
		n := calls.Add(1)
		if n == 1 {
			return "alice", "secret-1", nil
		}
		return "", "", errors.New("kms outage")
	}, time.Second)

	u, p := bridge()
	require.Equal(t, "alice", u)
	require.Equal(t, "secret-1", p)

	// Second call: provider errors but bridge serves the cached pair.
	u, p = bridge()
	assert.Equal(t, "alice", u)
	assert.Equal(t, "secret-1", p)
}

func TestUserPassBridge_FirstErrorReturnsEmpty(t *testing.T) {
	bridge := newUserPassBridge(func(context.Context) (string, string, error) {
		return "", "", errors.New("vault unreachable")
	}, time.Second)

	u, p := bridge()
	assert.Empty(t, u)
	assert.Empty(t, p)
}

func TestTokenBridge_AppliesTimeoutAndCaches(t *testing.T) {
	var calls atomic.Int64
	bridge := newTokenBridge(func(ctx context.Context) (string, error) {
		_, deadlineSet := ctx.Deadline()
		if !deadlineSet {
			t.Errorf("ctx must carry a deadline")
		}
		n := calls.Add(1)
		if n == 1 {
			return "tok-1", nil
		}
		return "", errors.New("transient")
	}, 200*time.Millisecond)

	require.Equal(t, "tok-1", bridge())
	assert.Equal(t, "tok-1", bridge(), "must fall back to cached value on error")
}

func TestTokenBridge_FirstErrorReturnsEmpty(t *testing.T) {
	bridge := newTokenBridge(func(context.Context) (string, error) {
		return "", errors.New("provider down")
	}, time.Second)
	assert.Empty(t, bridge())
}
