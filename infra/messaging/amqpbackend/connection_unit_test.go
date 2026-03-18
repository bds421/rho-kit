package amqpbackend

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Dial validation ---

func TestDial_EmptyURL_ReturnsError(t *testing.T) {
	_, err := Dial("", discardLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "amqp URL must not be empty")
}

func TestDial_NilLogger_ReturnsError(t *testing.T) {
	_, err := Dial("amqp://localhost", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "logger must not be nil")
}

func TestDial_InvalidURL_ReturnsError(t *testing.T) {
	_, err := Dial("not-a-valid-url", discardLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "amqp dial")
}

// --- DialOption constructors ---

func TestWithMaxReconnectAttempts_SetsValue(t *testing.T) {
	c := &Connection{}
	WithMaxReconnectAttempts(5)(c)
	assert.Equal(t, 5, c.maxReconnectAttempts)
}

func TestOnReconnect_SetsCallback(t *testing.T) {
	c := &Connection{}
	fn := func(_ Connector) error { return nil }
	OnReconnect(fn)(c)
	assert.NotNil(t, c.onReconnect)
}

func TestWithTLS_SetsConfig(t *testing.T) {
	c := &Connection{}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13}
	WithTLS(cfg)(c)
	assert.Equal(t, cfg, c.tlsConfig)
}

func TestWithLazyConnect_SetsFlag(t *testing.T) {
	c := &Connection{}
	WithLazyConnect()(c)
	assert.True(t, c.lazyConnect)
}

// --- Channel on nil connection ---

func TestChannel_NilConnection_ReturnsError(t *testing.T) {
	c := &Connection{
		closed:    make(chan struct{}),
		dead:      make(chan struct{}),
		connected: make(chan struct{}),
	}

	_, err := c.Channel()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection is not available")
}

// --- Close idempotent ---

func TestClose_NilConnection_NoError(t *testing.T) {
	c := &Connection{
		closed:    make(chan struct{}),
		dead:      make(chan struct{}),
		connected: make(chan struct{}),
	}

	err := c.Close()
	assert.NoError(t, err)

	// Second close should also be fine.
	err = c.Close()
	assert.NoError(t, err)
}

// --- Healthy on nil connection ---

func TestHealthy_NilConnection_ReturnsFalse(t *testing.T) {
	c := &Connection{
		closed:    make(chan struct{}),
		dead:      make(chan struct{}),
		connected: make(chan struct{}),
	}

	assert.False(t, c.Healthy())
}

// --- Healthy after dead ---

func TestHealthy_Dead_ReturnsFalse(t *testing.T) {
	dead := make(chan struct{})
	close(dead)
	c := &Connection{
		closed:    make(chan struct{}),
		dead:      dead,
		connected: make(chan struct{}),
	}

	assert.False(t, c.Healthy())
}

// --- Dead returns channel ---

func TestDead_ReturnsChannel(t *testing.T) {
	c := &Connection{
		dead: make(chan struct{}),
	}
	ch := c.Dead()
	assert.NotNil(t, ch)
}

// --- Connected returns channel ---

func TestConnected_ReturnsChannel(t *testing.T) {
	c := &Connection{
		connected: make(chan struct{}),
	}
	ch := c.Connected()
	assert.NotNil(t, ch)
}

// --- Dial with lazy connect ---

func TestDial_LazyConnect_ReturnsImmediately(t *testing.T) {
	// LazyConnect should return a connection immediately, even with an invalid URL.
	// The reconnect loop runs in the background.
	conn, err := Dial("amqp://invalid-host:99999", discardLogger(),
		WithLazyConnect(),
		WithMaxReconnectAttempts(1),
	)
	require.NoError(t, err)
	require.NotNil(t, conn)

	// Connection should not be healthy yet.
	assert.False(t, conn.Healthy())

	// Wait for the reconnect loop to give up (1 attempt with a 3s base delay).
	// We check Dead() becomes closed.
	select {
	case <-conn.Dead():
		// Expected: max reconnect attempts exhausted.
	case <-time.After(15 * time.Second):
		t.Fatal("expected Dead() channel to close after max attempts exhausted")
	}

	_ = conn.Close()
}

func TestDial_WithAllOptions(t *testing.T) {
	var reconnectCalled bool
	conn, err := Dial("amqp://invalid-host:99999", discardLogger(),
		WithMaxReconnectAttempts(1),
		WithLazyConnect(),
		OnReconnect(func(_ Connector) error {
			reconnectCalled = true
			return nil
		}),
		WithTLS(nil), // nil TLS config is valid (disables TLS)
	)
	require.NoError(t, err)
	require.NotNil(t, conn)

	// Wait for exhaustion.
	select {
	case <-conn.Dead():
	case <-time.After(15 * time.Second):
		t.Fatal("expected Dead() to close")
	}

	assert.False(t, reconnectCalled, "reconnect callback not called when connection never succeeds")
	_ = conn.Close()
}
