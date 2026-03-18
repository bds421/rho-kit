//go:build integration

package redis

import (
	"context"
	"log/slog"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/infra/redis/redistest"
)

func redisOpts(t *testing.T) *goredis.Options {
	t.Helper()
	url := redistest.Start(t)
	opts, err := goredis.ParseURL(url)
	require.NoError(t, err)
	return opts
}

// --- Connection Tests ---

func TestConnection_ConnectAndHealthy(t *testing.T) {
	opts := redisOpts(t)

	conn, err := Connect(opts, WithLogger(slog.Default()))
	require.NoError(t, err)
	defer conn.Close()

	assert.True(t, conn.Healthy())
}

func TestConnection_LazyConnect(t *testing.T) {
	opts := redisOpts(t)

	conn, err := Connect(opts, WithLazyConnect(), WithLogger(slog.Default()))
	require.NoError(t, err)
	defer conn.Close()

	// Wait for connection to be established.
	select {
	case <-conn.Connected():
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for connection")
	}

	assert.True(t, conn.Healthy())
}

func TestConnection_Close(t *testing.T) {
	opts := redisOpts(t)

	conn, err := Connect(opts, WithLogger(slog.Default()))
	require.NoError(t, err)

	err = conn.Close()
	require.NoError(t, err)

	assert.False(t, conn.Healthy())

	// Idempotent close.
	err = conn.Close()
	require.NoError(t, err)
}

func TestConnection_OnReconnect(t *testing.T) {
	opts := redisOpts(t)

	callbackDone := make(chan struct{})
	conn, err := Connect(opts,
		WithLazyConnect(),
		WithLogger(slog.Default()),
		WithOnReconnect(func(_ context.Context, _ *Connection) error {
			close(callbackDone)
			return nil
		}),
	)
	require.NoError(t, err)
	defer conn.Close()

	// Wait for the onReconnect callback to fire, not just Connected().
	select {
	case <-callbackDone:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for onReconnect callback")
	}

	assert.True(t, conn.Healthy())
}

// --- Health Check Tests ---

func TestHealthCheck_Healthy(t *testing.T) {
	opts := redisOpts(t)
	conn, err := Connect(opts)
	require.NoError(t, err)
	defer conn.Close()

	check := HealthCheck(conn)
	status := check.Check(context.Background())
	assert.Equal(t, "healthy", status)
	assert.False(t, check.Critical)
}

func TestCriticalHealthCheck_Healthy(t *testing.T) {
	opts := redisOpts(t)
	conn, err := Connect(opts)
	require.NoError(t, err)
	defer conn.Close()

	check := CriticalHealthCheck(conn)
	status := check.Check(context.Background())
	assert.Equal(t, "healthy", status)
	assert.True(t, check.Critical)
}

// --- Pool Metrics Test ---

func TestCollectPoolMetrics(t *testing.T) {
	opts := redisOpts(t)
	conn, err := Connect(opts)
	require.NoError(t, err)
	defer conn.Close()

	// Should not panic.
	CollectPoolMetrics(conn.Client(), "test")
}
