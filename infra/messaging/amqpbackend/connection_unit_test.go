package amqpbackend

import (
	"context"
	"crypto/tls"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Connect validation ---

func TestDial_EmptyURL_ReturnsError(t *testing.T) {
	_, err := Connect("", discardLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "amqp URL must not be empty")
}

func TestDial_NilLogger_ReturnsError(t *testing.T) {
	_, err := Connect("amqp://localhost", nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "logger must not be nil")
}

func TestDial_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		_, _ = Connect("amqp://localhost", discardLogger(), nil)
	})
}

func TestDial_InvalidURL_ReturnsError(t *testing.T) {
	_, err := Connect("not-a-valid-url", discardLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "absolute amqp(s) URL")
}

func TestDial_RejectsPlaintextWithoutOptIn(t *testing.T) {
	_, err := Connect("amqp://localhost:5672/", discardLogger())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithoutTLS")
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

func TestWithoutTLS_SetsFlag(t *testing.T) {
	c := &Connection{}
	WithoutTLS()(c)
	assert.True(t, c.allowPlaintext)
}

func TestWithLazyConnect_SetsFlag(t *testing.T) {
	c := &Connection{}
	WithLazyConnect()(c)
	assert.True(t, c.lazyConnect)
}

func TestWithURLProvider_PanicsOnNil(t *testing.T) {
	assert.Panics(t, func() {
		WithURLProvider(nil)
	})
}

func TestWithURLProviderTimeout_PanicsOnNonPositive(t *testing.T) {
	assert.Panics(t, func() {
		WithURLProviderTimeout(0)
	})
}

func TestResolveDialURL_UsesProvider(t *testing.T) {
	c := &Connection{
		allowPlaintext: true,
		urlProvider: func(context.Context) (string, error) {
			return "amqp://user:rotated@rabbit:5672/vhost", nil
		},
	}

	got, err := c.resolveDialURL(t.Context())

	require.NoError(t, err)
	assert.Equal(t, "amqp://user:rotated@rabbit:5672/vhost", got)
}

func TestResolveDialURL_ProviderReceivesTimeoutContext(t *testing.T) {
	var sawDeadline bool
	c := &Connection{
		allowPlaintext:     true,
		urlProviderTimeout: 10 * time.Second,
		urlProvider: func(ctx context.Context) (string, error) {
			deadline, ok := ctx.Deadline()
			sawDeadline = ok && time.Until(deadline) > 0
			return "amqp://user:rotated@rabbit:5672/vhost", nil
		},
	}

	_, err := c.resolveDialURL(t.Context())

	require.NoError(t, err)
	assert.True(t, sawDeadline)
}

func TestResolveDialURL_ProviderErrorDoesNotRenderCause(t *testing.T) {
	cause := fmt.Errorf("vault denied token=secret")
	c := &Connection{
		allowPlaintext: true,
		urlProvider: func(context.Context) (string, error) {
			return "", cause
		},
	}

	_, err := c.resolveDialURL(t.Context())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "amqp URL provider failed")
	assert.NotContains(t, err.Error(), "token=secret")
	assert.ErrorIs(t, err, cause)
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

	err := c.Stop(context.Background())
	assert.NoError(t, err)

	// Second close should also be fine.
	err = c.Stop(context.Background())
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
	assert.NoError(t, nilConn.Stop(context.Background()))

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
	assert.NoError(t, zero.Stop(context.Background()))

	err = zero.WaitForConnection(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")
}

// --- Connect with lazy connect ---

func TestDial_LazyConnect_ReturnsImmediately(t *testing.T) {
	// LazyConnect should return a connection immediately, even with an invalid URL.
	// The reconnect loop runs in the background.
	conn, err := Connect("amqp://invalid-host:99999", discardLogger(),
		WithoutTLS(),
		WithLazyConnect(),
		WithMaxReconnectAttempts(1),
		withFastReconnect(),
	)
	require.NoError(t, err)
	require.NotNil(t, conn)

	// Connection should not be healthy yet.
	assert.False(t, conn.Healthy())

	// Wait for the reconnect loop to give up after one attempt. The test-only
	// timing option keeps production's 3s worker backoff out of this unit test.
	// We check Dead() becomes closed.
	select {
	case <-conn.Dead():
		// Expected: max reconnect attempts exhausted.
	case <-time.After(15 * time.Second):
		t.Fatal("expected Dead() channel to close after max attempts exhausted")
	}

	_ = conn.Stop(context.Background())
}

// --- drainPendingReconnect (lost reconnect signal window) ---

func TestDrainPendingReconnect_NoSignal_ReleasesFlagAndStops(t *testing.T) {
	c := &Connection{reconnectSignal: make(chan struct{}, 1)}
	c.reconnecting.Store(true)

	again := c.drainPendingReconnect()

	assert.False(t, again, "no queued signal — loop must stop")
	assert.False(t, c.reconnecting.Load(), "flag must be released when stopping")
}

func TestDrainPendingReconnect_PendingSignal_ReacquiresFlagAndContinues(t *testing.T) {
	c := &Connection{reconnectSignal: make(chan struct{}, 1)}
	c.reconnecting.Store(true)
	// A watcher queued a signal in the window between reconnect()'s final
	// drain and the flag being cleared.
	c.reconnectSignal <- struct{}{}

	again := c.drainPendingReconnect()

	assert.True(t, again, "queued signal must re-arm the reconnect loop")
	assert.True(t, c.reconnecting.Load(), "flag must be re-acquired so no overlapping loop starts")
	assert.Len(t, c.reconnectSignal, 0, "the queued signal must be consumed")
}

// TestStartReconnect_DoesNotDropSignalRacingFinalDrain exercises the
// lost-signal window end to end: with the loop already finishing (flag set,
// no goroutine), a watcher's startReconnect must not silently drop the
// reconnect request. Before the fix the deferred Store(false) cleared the
// flag with the signal still buffered and no loop to pick it up.
func TestStartReconnect_DoesNotDropSignalRacingFinalDrain(t *testing.T) {
	c := &Connection{
		logger:          discardLogger(),
		closed:          make(chan struct{}),
		dead:            make(chan struct{}),
		connected:       make(chan struct{}),
		reconnectSignal: make(chan struct{}, 1),
	}
	// Simulate a reconnect loop that has finished its work and is about to
	// release ownership: flag still true, goroutine not running.
	c.reconnecting.Store(true)

	// Watcher observes a drop and tries to trigger reconnect; CAS fails
	// because the flag is still set, so it queues a signal.
	c.startReconnect()
	assert.Len(t, c.reconnectSignal, 1, "queued signal must be buffered when CAS fails")

	// The finishing loop now releases ownership via drainPendingReconnect.
	// It must observe the buffered signal and re-acquire rather than drop it.
	again := c.drainPendingReconnect()
	assert.True(t, again, "the finishing loop must re-run reconnect for the queued signal")
	assert.True(t, c.reconnecting.Load())
}

// --- closing (zombie connection guard after Stop) ---

func TestClosing_OpenConnection_ReturnsFalse(t *testing.T) {
	c := &Connection{closed: make(chan struct{})}
	assert.False(t, c.closing())
}

func TestClosing_AfterStop_ReturnsTrue(t *testing.T) {
	c := &Connection{closed: make(chan struct{})}
	close(c.closed)
	assert.True(t, c.closing(), "after Stop closes c.closed, the reconnect loop must abandon a freshly dialed conn")
}

func TestClosing_NilChannel_ReturnsFalse(t *testing.T) {
	c := &Connection{}
	assert.False(t, c.closing())
}

func TestDial_WithAllOptions(t *testing.T) {
	var reconnectCalled bool
	conn, err := Connect("amqp://invalid-host:99999", discardLogger(),
		WithMaxReconnectAttempts(1),
		WithLazyConnect(),
		withFastReconnect(),
		WithoutTLS(),
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
	_ = conn.Stop(context.Background())
}

// withFastReconnect changes only the private policy used by same-package
// tests. Connect still installs retry.WorkerPolicy for every production call.
func withFastReconnect() DialOption {
	return func(c *Connection) {
		c.reconnectPolicy.BaseDelay = time.Millisecond
		c.reconnectPolicy.MaxDelay = time.Millisecond
		c.reconnectPolicy.Factor = 1
		c.reconnectPolicy.Jitter = 0
	}
}

func TestStop_AlreadyCancelledCtx_StillClosesConnection(t *testing.T) {
	c := &Connection{
		closed:          make(chan struct{}),
		dead:            make(chan struct{}),
		connected:       make(chan struct{}),
		reconnectSignal: make(chan struct{}, 1),
		logger:          discardLogger(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.Stop(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)

	select {
	case <-c.closed:
	default:
		t.Fatal("Stop with cancelled ctx must still close c.closed so reconnect stops")
	}
}

func TestWaitForConnection_ObservesDead(t *testing.T) {
	c := &Connection{
		closed: make(chan struct{}),
		dead:   make(chan struct{}),
	}
	close(c.dead)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.WaitForConnection(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permanently lost")
}

func TestDrainPendingReconnect_WhenDead_DoesNotRearm(t *testing.T) {
	c := &Connection{
		logger:          discardLogger(),
		closed:          make(chan struct{}),
		dead:            make(chan struct{}),
		reconnectSignal: make(chan struct{}, 1),
	}
	close(c.dead)
	c.reconnecting.Store(true)
	c.reconnectSignal <- struct{}{}

	again := c.drainPendingReconnect()
	assert.False(t, again)
	assert.False(t, c.reconnecting.Load())
	assert.Len(t, c.reconnectSignal, 0)
}

func TestStartReconnect_WhenDead_Noop(t *testing.T) {
	c := &Connection{
		logger:          discardLogger(),
		closed:          make(chan struct{}),
		dead:            make(chan struct{}),
		reconnectSignal: make(chan struct{}, 1),
	}
	close(c.dead)
	c.startReconnect()
	assert.False(t, c.reconnecting.Load())
}
