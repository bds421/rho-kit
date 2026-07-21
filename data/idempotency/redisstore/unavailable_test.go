package redisstore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/data/v2/idempotency"
)

func TestErrStoreUnavailable_IsDependencyUnavailable(t *testing.T) {
	require.True(t, apperror.IsUnavailable(ErrStoreUnavailable))

	ue, ok := apperror.AsUnavailable(ErrStoreUnavailable)
	require.True(t, ok)
	assert.Equal(t, "idempotency", ue.Dependency)
	assert.True(t, IsStoreUnavailable(ErrStoreUnavailable))
}

func TestIsStoreUnavailable(t *testing.T) {
	assert.False(t, IsStoreUnavailable(nil))
	assert.False(t, IsStoreUnavailable(errors.New("plain")))
	other := apperror.NewDependencyUnavailable("kms", "down", nil)
	assert.False(t, IsStoreUnavailable(other))
	assert.True(t, IsStoreUnavailable(ErrStoreUnavailable))
}

type fakeServerErr string

func (e fakeServerErr) Error() string { return string(e) }
func (fakeServerErr) RedisError()     {}

func TestTranslateUnavailable(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantUnav bool
	}{
		{"nil", nil, false},
		{"plain", errors.New("oops"), false},
		{"sentinel error", errors.New("sentinel"), false},
		{"READONLY reply", fakeServerErr("READONLY You can't write against a read only replica."), true},
		{"pool closed", goredis.ErrClosed, true},
		{"pool exhausted", goredis.ErrPoolExhausted, true},
		{"pool timeout", goredis.ErrPoolTimeout, true},
		{"net dial error", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		// Context errors are caller-driven cancellation, not a dependency
		// outage. They must pass through unchanged even though
		// context.DeadlineExceeded itself satisfies the net.Error interface.
		{"context deadline exceeded", context.DeadlineExceeded, false},
		{"context canceled", context.Canceled, false},
		{"wrapped context deadline", fmt.Errorf("redis: %w", context.DeadlineExceeded), false},
		{"wrapped context canceled", fmt.Errorf("redis: %w", context.Canceled), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := translateUnavailable(tc.err)
			if tc.err == nil {
				assert.Nil(t, got)
				return
			}
			if !tc.wantUnav {
				assert.Equal(t, tc.err, got)
				assert.False(t, IsStoreUnavailable(got))
				return
			}
			assert.True(t, IsStoreUnavailable(got))
			assert.True(t, errors.Is(got, tc.err), "translated error must preserve cause")
		})
	}
}

func TestTryLock_TranslatesPoolErrorsToUnavailable(t *testing.T) {
	// Hit an unroutable address with a quick timeout to force a connection
	// error without depending on miniredis.
	client := goredis.NewClient(&goredis.Options{
		Addr:               "127.0.0.1:1",
		DialTimeout:        50 * time.Millisecond,
		MaxRetries:         -1,
		DialerRetries:      1,
		DialerRetryTimeout: time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })

	store := New(client)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, _, _, err := store.TryLock(ctx, "k-unavailable", nil, time.Second)
	require.Error(t, err)
	assert.True(t, IsStoreUnavailable(err), "expected unavailable, got %v", err)
}

func TestGet_TranslatesPoolErrorsToUnavailable(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{
		Addr:               "127.0.0.1:1",
		DialTimeout:        50 * time.Millisecond,
		MaxRetries:         -1,
		DialerRetries:      1,
		DialerRetryTimeout: time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })

	store := New(client)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, _, err := store.Get(ctx, "k-unavailable", nil)
	require.Error(t, err)
	assert.True(t, IsStoreUnavailable(err), "expected unavailable, got %v", err)
}

func TestSet_TranslatesPoolErrorsToUnavailable(t *testing.T) {
	client := goredis.NewClient(&goredis.Options{
		Addr:               "127.0.0.1:1",
		DialTimeout:        50 * time.Millisecond,
		MaxRetries:         -1,
		DialerRetries:      1,
		DialerRetryTimeout: time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })

	store := New(client)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	resp := idempotency.CachedResponse{StatusCode: 200, Body: []byte("ok")}
	err := store.Set(ctx, "k-unavailable", "tok", resp, time.Second)
	require.Error(t, err)
	assert.True(t, IsStoreUnavailable(err), "expected unavailable, got %v", err)
}
