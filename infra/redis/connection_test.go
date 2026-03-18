package redis

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConnect_Success(t *testing.T) {
	mr := miniredis.RunT(t)
	opts := &goredis.Options{Addr: mr.Addr()}

	conn, err := Connect(opts, WithLogger(slog.Default()))
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	assert.True(t, conn.Healthy())
	assert.True(t, conn.WasConnected())
	assert.NotNil(t, conn.Client())
}

func TestConnect_Failure(t *testing.T) {
	opts := &goredis.Options{Addr: "localhost:1"} // bad port

	_, err := Connect(opts, WithLogger(slog.Default()))
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "redis connect")
}

func TestConnect_LazyConnect(t *testing.T) {
	mr := miniredis.RunT(t)
	opts := &goredis.Options{Addr: mr.Addr()}

	conn, err := Connect(opts,
		WithLazyConnect(),
		WithLogger(slog.Default()),
		WithHealthInterval(100*time.Millisecond),
	)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Wait for connection.
	select {
	case <-conn.Connected():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for lazy connection")
	}

	assert.True(t, conn.Healthy())
}

func TestConnect_Close(t *testing.T) {
	mr := miniredis.RunT(t)
	opts := &goredis.Options{Addr: mr.Addr()}

	conn, err := Connect(opts, WithLogger(slog.Default()))
	require.NoError(t, err)

	err = conn.Close()
	require.NoError(t, err)
	assert.False(t, conn.Healthy())

	// Idempotent close.
	err = conn.Close()
	require.NoError(t, err)
}

func TestConnect_OnReconnect(t *testing.T) {
	mr := miniredis.RunT(t)
	opts := &goredis.Options{Addr: mr.Addr()}

	callbackDone := make(chan struct{})
	conn, err := Connect(opts,
		WithLazyConnect(),
		WithLogger(slog.Default()),
		WithHealthInterval(100*time.Millisecond),
		WithOnReconnect(func(_ context.Context, _ *Connection) error {
			close(callbackDone)
			return nil
		}),
	)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	select {
	case <-callbackDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for onReconnect")
	}
}

func TestConnect_MaxReconnectAttempts(t *testing.T) {
	opts := &goredis.Options{Addr: "localhost:1"} // bad port

	conn, err := Connect(opts,
		WithLazyConnect(),
		WithLogger(slog.Default()),
		WithMaxReconnectAttempts(1),
		WithHealthInterval(100*time.Millisecond),
	)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	select {
	case <-conn.Dead():
		// Expected.
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for dead signal")
	}

	assert.False(t, conn.Healthy())
}

func TestConnect_Dead_NotClosed(t *testing.T) {
	mr := miniredis.RunT(t)
	opts := &goredis.Options{Addr: mr.Addr()}

	conn, err := Connect(opts, WithLogger(slog.Default()))
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Dead should not fire on a healthy connection.
	select {
	case <-conn.Dead():
		t.Fatal("dead should not fire on healthy connection")
	case <-time.After(100 * time.Millisecond):
		// Expected.
	}
}

func TestWithHealthInterval_IgnoresInvalid(t *testing.T) {
	mr := miniredis.RunT(t)
	opts := &goredis.Options{Addr: mr.Addr()}

	conn, err := Connect(opts,
		WithLogger(slog.Default()),
		WithHealthInterval(0),                    // invalid: zero
		WithHealthInterval(-time.Second),         // invalid: negative
		WithHealthInterval(100*time.Millisecond), // valid: should take effect
	)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// healthInterval should be the last valid value (100ms).
	assert.Equal(t, 100*time.Millisecond, conn.healthInterval)
}

func TestConnect_OnReconnect_ErrorDoesNotAffectHealth(t *testing.T) {
	mr := miniredis.RunT(t)
	opts := &goredis.Options{Addr: mr.Addr()}

	callbackDone := make(chan struct{})
	conn, err := Connect(opts,
		WithLazyConnect(),
		WithLogger(slog.Default()),
		WithHealthInterval(100*time.Millisecond),
		WithOnReconnect(func(_ context.Context, _ *Connection) error {
			close(callbackDone)
			return errors.New("callback failed")
		}),
	)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	// Wait for callback to fire.
	select {
	case <-callbackDone:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for onReconnect")
	}

	// Connection should still be healthy despite the callback error.
	time.Sleep(200 * time.Millisecond) // let health check run
	assert.True(t, conn.Healthy())
}

func TestConnect_WithInstance(t *testing.T) {
	mr := miniredis.RunT(t)
	opts := &goredis.Options{Addr: mr.Addr()}

	conn, err := Connect(opts,
		WithLogger(slog.Default()),
		WithInstance("cache"),
	)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	assert.Equal(t, "cache", conn.instance)
}

func TestConnect_DefaultInstance(t *testing.T) {
	mr := miniredis.RunT(t)
	opts := &goredis.Options{Addr: mr.Addr()}

	conn, err := Connect(opts, WithLogger(slog.Default()))
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	assert.Equal(t, "default", conn.instance)
}

func TestWithMaxReconnectAttempts_IgnoresNegative(t *testing.T) {
	mr := miniredis.RunT(t)
	opts := &goredis.Options{Addr: mr.Addr()}

	conn, err := Connect(opts,
		WithLogger(slog.Default()),
		WithMaxReconnectAttempts(-1),
	)
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	assert.Equal(t, 0, conn.maxReconnectAttempts) // default (unlimited)
}

func TestWithInstance_PanicsOnInvalidName(t *testing.T) {
	tests := []struct {
		name     string
		instance string
	}{
		{"empty", ""},
		{"null byte", "bad\x00name"},
		{"newline", "bad\nname"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Panics(t, func() {
				// WithInstance returns an option func; the panic occurs
				// when the option is applied to a Connection.
				opt := WithInstance(tt.instance)
				opt(&Connection{})
			})
		})
	}
}
