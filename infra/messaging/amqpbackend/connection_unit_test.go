package amqpbackend

import (
	"crypto/tls"
	"fmt"
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

func TestDial_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		_, _ = Dial("amqp://localhost", discardLogger(), nil)
	})
}

func TestDial_InvalidURL_ReturnsError(t *testing.T) {
	_, err := Dial("not-a-valid-url", discardLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute amqp(s) URL")
}

func TestDial_RejectsPlaintextWithoutOptIn(t *testing.T) {
	_, err := Dial("amqp://localhost:5672/", discardLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithAllowPlaintext")
}

func TestNormalizeDialURL_RewritesAMQPWhenTLSConfigured(t *testing.T) {
	got, err := normalizeDialURL("amqp://user:pass@rabbit:5672/vhost", true, false)
	require.NoError(t, err)
	assert.Equal(t, "amqps://user:pass@rabbit:5672/vhost", got)
}

func TestNormalizeDialURL_AllowsPlaintextWithExplicitOptIn(t *testing.T) {
	got, err := normalizeDialURL("amqp://rabbit:5672/", false, true)
	require.NoError(t, err)
	assert.Equal(t, "amqp://rabbit:5672/", got)
}

func TestNormalizeDialURL_AcceptsAMQPSWithoutCustomTLS(t *testing.T) {
	got, err := normalizeDialURL("amqps://rabbit:5671/", false, false)
	require.NoError(t, err)
	assert.Equal(t, "amqps://rabbit:5671/", got)
}

func TestNormalizeDialURL_ParseErrorDoesNotEchoValue(t *testing.T) {
	_, err := normalizeDialURL("amqps://rabbit/%zz?token=secret-token", false, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "amqp URL is invalid")
	assert.NotContains(t, err.Error(), "secret-token")
	assert.NotContains(t, err.Error(), "token=")
	assert.NotContains(t, err.Error(), "%zz")
}

func TestNormalizeDialURL_SchemeErrorDoesNotEchoValue(t *testing.T) {
	_, err := normalizeDialURL("secret-token://rabbit", false, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "amqp URL scheme must be amqp or amqps")
	assert.NotContains(t, err.Error(), "secret-token")
}

func TestSanitizeURL_DropsCredentialsQueryAndFragment(t *testing.T) {
	got := sanitizeURL("amqps://token-user:secret@rabbit:5671/vhost?token=query-secret#frag")
	assert.Contains(t, got, "rabbit:5671")
	assert.NotContains(t, got, "token-user")
	assert.NotContains(t, got, "secret")
	assert.NotContains(t, got, "query-secret")
	assert.NotContains(t, got, "frag")
}

// --- DialOption constructors ---

func TestWithMaxReconnectAttempts_SetsValue(t *testing.T) {
	c := &Connection{}
	WithMaxReconnectAttempts(5)(c)
	assert.Equal(t, 5, c.maxReconnectAttempts)
}

func TestWithMaxReconnectAttempts_PanicsOnNegative(t *testing.T) {
	require.Panics(t, func() {
		WithMaxReconnectAttempts(-1)
	})
}

func TestWithMaxDLQConsecutiveFailures_PanicsOnNonPositive(t *testing.T) {
	for _, n := range []int{0, -1} {
		t.Run(fmt.Sprintf("%d", n), func(t *testing.T) {
			require.Panics(t, func() {
				WithMaxDLQConsecutiveFailures(n)
			})
		})
	}
}

func TestOnReconnect_SetsCallback(t *testing.T) {
	c := &Connection{}
	fn := func(_ Connector) error { return nil }
	OnReconnect(fn)(c)
	assert.NotNil(t, c.onReconnect)
}

func TestOnReconnect_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() {
		OnReconnect(nil)
	})
}

func TestCallOnReconnect_ConvertsPanicToError(t *testing.T) {
	c := &Connection{
		logger: discardLogger(),
		onReconnect: func(Connector) error {
			panic("callback exploded")
		},
	}

	err := c.callOnReconnect()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "onReconnect panic")
}

func TestWithTLS_ClonesConfigAndEnforcesFloor(t *testing.T) {
	c := &Connection{}
	cfg := &tls.Config{
		NextProtos: []string{"h2"},
		ServerName: "rabbit.internal.test",
	}
	cfg.MinVersion = minimumTLSVersion - 1
	WithTLS(cfg)(c)
	cfg.NextProtos[0] = "http/1.1"
	require.NotNil(t, c.tlsConfig)
	assert.NotSame(t, cfg, c.tlsConfig)
	assert.Equal(t, uint16(minimumTLSVersion-1), cfg.MinVersion)
	assert.Equal(t, uint16(minimumTLSVersion), c.tlsConfig.MinVersion)
	assert.Equal(t, []string{"h2"}, c.tlsConfig.NextProtos)
	assert.Equal(t, "rabbit.internal.test", c.tlsConfig.ServerName)
}

func TestWithTLS_PanicsWhenMaxVersionBelowFloor(t *testing.T) {
	cfg := &tls.Config{MaxVersion: minimumTLSVersion - 1}

	assert.Panics(t, func() {
		WithTLS(cfg)
	})
}

func TestWithTLS_NilConfigDisablesCustomTLS(t *testing.T) {
	c := &Connection{}
	WithTLS(nil)(c)
	assert.Nil(t, c.tlsConfig)
}

func TestWithAllowPlaintext_SetsFlag(t *testing.T) {
	c := &Connection{}
	WithAllowPlaintext()(c)
	assert.True(t, c.allowPlaintext)
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

func TestConnection_InvalidReceiverSafety(t *testing.T) {
	var nilConn *Connection
	assert.False(t, nilConn.Healthy())
	assert.Nil(t, nilConn.Dead())
	assert.Nil(t, nilConn.Connected())
	assert.NoError(t, nilConn.Close())

	_, err := nilConn.Channel()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "connection is not available")

	err = nilConn.WaitForConnection(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")

	zero := &Connection{}
	assert.False(t, zero.Healthy())
	assert.Nil(t, zero.Dead())
	assert.Nil(t, zero.Connected())
	assert.NoError(t, zero.Close())

	err = zero.WaitForConnection(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")
}

// --- Dial with lazy connect ---

func TestDial_LazyConnect_ReturnsImmediately(t *testing.T) {
	// LazyConnect should return a connection immediately, even with an invalid URL.
	// The reconnect loop runs in the background.
	conn, err := Dial("amqp://invalid-host:99999", discardLogger(),
		WithAllowPlaintext(),
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
		WithAllowPlaintext(),
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
