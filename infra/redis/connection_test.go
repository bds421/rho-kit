package redis

import (
	"bytes"
	"context"
	"crypto/tls"
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

func TestConnect_PanicsOnNilOptions(t *testing.T) {
	require.Panics(t, func() {
		_, _ = Connect(nil)
	})
}

func TestConnect_ClonesOptionsAndTLSConfig(t *testing.T) {
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS10,
		NextProtos: []string{"h2"},
		ServerName: "before.example",
	}
	opts := &goredis.Options{Addr: "127.0.0.1:1", TLSConfig: tlsConfig}

	conn, err := Connect(opts, WithLazyConnect())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	client, ok := conn.Client().(*goredis.Client)
	require.True(t, ok, "expected single-node client")
	got := client.Options()

	opts.Addr = "mutated:6379"
	tlsConfig.ServerName = "after.example"
	tlsConfig.NextProtos[0] = "http/1.1"

	assert.Equal(t, "127.0.0.1:1", got.Addr)
	require.NotNil(t, got.TLSConfig)
	assert.Equal(t, "before.example", got.TLSConfig.ServerName)
	assert.Equal(t, []string{"h2"}, got.TLSConfig.NextProtos)
	assert.Equal(t, uint16(tls.VersionTLS12), got.TLSConfig.MinVersion)
	assert.NotSame(t, tlsConfig, got.TLSConfig)
}

func TestConnect_RejectsTLSMaxVersionBelowFloor(t *testing.T) {
	_, err := Connect(&goredis.Options{
		Addr:      "127.0.0.1:1",
		TLSConfig: &tls.Config{MaxVersion: tls.VersionTLS11},
	}, WithLazyConnect())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS MaxVersion")
}

func TestConnectUniversal_PanicsOnNilOptions(t *testing.T) {
	require.Panics(t, func() {
		_, _ = ConnectUniversal(nil)
	})
}

func TestConnectUniversal_ClonesOptionsAndTLSConfig(t *testing.T) {
	tlsConfig := &tls.Config{ServerName: "before.example"}
	opts := &goredis.UniversalOptions{
		Addrs:     []string{"127.0.0.1:1"},
		TLSConfig: tlsConfig,
	}

	conn, err := ConnectUniversal(opts, WithLazyConnect())
	require.NoError(t, err)
	defer func() { _ = conn.Close() }()

	client, ok := conn.Client().(*goredis.Client)
	require.True(t, ok, "expected single-node client")
	got := client.Options()

	opts.Addrs[0] = "mutated:6379"
	tlsConfig.ServerName = "after.example"

	assert.Equal(t, "127.0.0.1:1", got.Addr)
	require.NotNil(t, got.TLSConfig)
	assert.Equal(t, "before.example", got.TLSConfig.ServerName)
	assert.Equal(t, uint16(tls.VersionTLS12), got.TLSConfig.MinVersion)
	assert.NotSame(t, tlsConfig, got.TLSConfig)
}

func TestConnect_PanicsOnNilConnOption(t *testing.T) {
	mr := miniredis.RunT(t)
	opts := &goredis.Options{Addr: mr.Addr()}

	require.Panics(t, func() {
		_, _ = Connect(opts, nil)
	})
}

func TestConnectInternal_PanicsOnNilClient(t *testing.T) {
	require.Panics(t, func() {
		_, _ = connectInternal(nil)
	})
}

func TestConnection_InvalidReceiverSafety(t *testing.T) {
	var nilConn *Connection
	assert.Nil(t, nilConn.Client())
	assert.False(t, nilConn.Healthy())
	assert.False(t, nilConn.WasConnected())
	assert.Nil(t, nilConn.Connected())
	assert.Nil(t, nilConn.Dead())
	assert.NoError(t, nilConn.Close())

	zero := &Connection{}
	assert.Nil(t, zero.Client())
	assert.False(t, zero.Healthy())
	assert.False(t, zero.WasConnected())
	assert.Nil(t, zero.Connected())
	assert.Nil(t, zero.Dead())
	assert.NoError(t, zero.Close())
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

func TestWithHealthInterval_PanicsOnInvalid(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		t.Run(d.String(), func(t *testing.T) {
			require.Panics(t, func() {
				WithHealthInterval(d)
			})
		})
	}
}

func TestWithHealthInterval_AppliesValidValue(t *testing.T) {
	mr := miniredis.RunT(t)
	opts := &goredis.Options{Addr: mr.Addr()}

	conn, err := Connect(opts,
		WithLogger(slog.Default()),
		WithHealthInterval(100*time.Millisecond),
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

func TestFireOnReconnect_ErrorLogRedactsCallbackError(t *testing.T) {
	var logs bytes.Buffer
	started := make(chan struct{}, 1)
	c := &Connection{
		closed:             make(chan struct{}),
		logger:             slog.New(slog.NewTextHandler(&logs, nil)),
		onReconnectTimeout: time.Second,
		onReconnect: func(context.Context, *Connection) error {
			close(started)
			return errors.New("callback token=tenant-secret")
		},
	}
	defer close(c.closed)

	c.fireOnReconnect()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconnect callback")
	}
	require.Eventually(t, func() bool {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return !c.reconnecting
	}, time.Second, 10*time.Millisecond)

	got := logs.String()
	assert.Contains(t, got, "redis onReconnect callback failed")
	assert.Contains(t, got, "<redacted error")
	assert.NotContains(t, got, "tenant-secret")
}

func TestFireOnReconnect_TimeoutSuppressesOverlappingCallbacks(t *testing.T) {
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	c := &Connection{
		closed:             make(chan struct{}),
		logger:             slog.Default(),
		onReconnectTimeout: 10 * time.Millisecond,
		onReconnect: func(context.Context, *Connection) error {
			started <- struct{}{}
			<-release
			return nil
		},
	}
	defer close(c.closed)

	c.fireOnReconnect()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for first reconnect callback")
	}

	require.Eventually(t, func() bool {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return c.reconnecting
	}, time.Second, 10*time.Millisecond)

	time.Sleep(50 * time.Millisecond)
	c.fireOnReconnect()
	select {
	case <-started:
		t.Fatal("second reconnect callback started while the first callback was still running")
	default:
	}

	close(release)
	require.Eventually(t, func() bool {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return !c.reconnecting
	}, time.Second, 10*time.Millisecond)

	c.fireOnReconnect()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconnect callback after first callback returned")
	}
}

func TestFireOnReconnect_PanicClearsInProgress(t *testing.T) {
	started := make(chan struct{}, 2)
	c := &Connection{
		closed:             make(chan struct{}),
		logger:             slog.Default(),
		onReconnectTimeout: time.Second,
		onReconnect: func(context.Context, *Connection) error {
			started <- struct{}{}
			panic("callback exploded")
		},
	}
	defer close(c.closed)

	c.fireOnReconnect()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for reconnect callback")
	}
	require.Eventually(t, func() bool {
		c.mu.RLock()
		defer c.mu.RUnlock()
		return !c.reconnecting
	}, time.Second, 10*time.Millisecond)

	c.fireOnReconnect()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("reconnect callback did not restart after panic")
	}
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

func TestWithMaxReconnectAttempts_PanicsOnNegative(t *testing.T) {
	require.Panics(t, func() {
		WithMaxReconnectAttempts(-1)
	})
}

func TestWithLogger_NilNormalizesToDefault(t *testing.T) {
	c := &Connection{}
	WithLogger(nil)(c)
	assert.NotNil(t, c.logger)
}

func TestWithOnReconnect_PanicsOnNilCallback(t *testing.T) {
	require.Panics(t, func() {
		WithOnReconnect(nil)
	})
}

func TestWithOnReconnectTimeout_PanicsOnInvalid(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second} {
		t.Run(d.String(), func(t *testing.T) {
			require.Panics(t, func() {
				WithOnReconnectTimeout(d)
			})
		})
	}
}

func TestWithOnReconnectTimeout_AppliesValidValue(t *testing.T) {
	c := &Connection{}
	WithOnReconnectTimeout(250 * time.Millisecond)(c)
	assert.Equal(t, 250*time.Millisecond, c.onReconnectTimeout)
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
			assert.PanicsWithValue(t, "redis: invalid instance name", func() {
				// WithInstance returns an option func; the panic occurs
				// when the option is applied to a Connection.
				opt := WithInstance(tt.instance)
				opt(&Connection{})
			})
		})
	}
}
