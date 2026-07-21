package websocket_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/httpx/websocket/v2"
)

func TestHandle_PanicsWithoutHandler(t *testing.T) {
	assert.Panics(t, func() {
		websocket.Handle()
	})
}

func TestHandle_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		websocket.Handle(nil)
	})
}

func TestHandle_RoundTrip(t *testing.T) {
	reg := prometheus.NewRegistry()
	called := make(chan struct{}, 1)

	handler := websocket.Handle(
		websocket.WithHandler(func(ctx context.Context, c *websocket.Conn) error {
			defer func() { called <- struct{}{} }()
			typ, payload, err := c.ReadMessage()
			if err != nil {
				return err
			}
			if typ != websocket.MessageText {
				t.Errorf("expected text message, got %v", typ)
			}
			if string(payload) != "ping" {
				t.Errorf("expected ping, got %q", payload)
			}
			return c.WriteMessage(websocket.MessageText, []byte("pong"))
		}),
		websocket.WithMetrics(reg),
	)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close(coderws.StatusNormalClosure, "") }()

	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte("ping")))

	typ, payload, err := conn.Read(ctx)
	require.NoError(t, err)
	assert.Equal(t, coderws.MessageText, typ)
	assert.Equal(t, "pong", string(payload))

	require.NoError(t, conn.Close(coderws.StatusNormalClosure, "done"))

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not invoked")
	}
}

func TestHandle_RecoversPanic(t *testing.T) {
	reg := prometheus.NewRegistry()

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, _ *websocket.Conn) error {
			panic("boom")
		}),
		websocket.WithMetrics(reg),
	)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close(coderws.StatusNormalClosure, "") }()

	_, _, readErr := conn.Read(ctx)
	require.Error(t, readErr)
	closeStatus := coderws.CloseStatus(readErr)
	assert.Equal(t, coderws.StatusInternalError, closeStatus)
}

func TestHandle_HandlerErrorClosesWithInternalError(t *testing.T) {
	reg := prometheus.NewRegistry()

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, _ *websocket.Conn) error {
			return errors.New("application failure")
		}),
		websocket.WithMetrics(reg),
	)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close(coderws.StatusNormalClosure, "") }()

	_, _, readErr := conn.Read(ctx)
	require.Error(t, readErr)
	closeStatus := coderws.CloseStatus(readErr)
	assert.Equal(t, coderws.StatusInternalError, closeStatus)
}

// TestHandle_NormalClosureReturnedByHandlerIsNotInternalError asserts
// that the natural handler pattern — read in a loop and return the read
// error on disconnect — does NOT escalate a routine client close into a
// StatusInternalError close. When the returned error carries a normal
// (1000) or going-away (1001) close status the connection must close
// with that same code, not 1011.
func TestHandle_NormalClosureReturnedByHandlerIsNotInternalError(t *testing.T) {
	reg := prometheus.NewRegistry()

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, c *websocket.Conn) error {
			// Block on a read; when the peer closes normally the read
			// returns a redacted close error which we surface verbatim,
			// exactly as the package's own round-trip handlers do.
			_, _, err := c.ReadMessage()
			return err
		}),
		websocket.WithMetrics(reg),
	)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)

	// Client performs a normal close. The server handler observes the
	// read error and returns it.
	require.NoError(t, conn.Close(coderws.StatusNormalClosure, "bye"))

	// The server's close handshake must not be StatusInternalError. We
	// inspect the close metric: a 1011 here would mean the kit
	// escalated a routine disconnect into a handler-error close.
	require.Eventually(t, func() bool {
		families, gerr := reg.Gather()
		if gerr != nil {
			return false
		}
		for _, f := range families {
			if f.GetName() != "httpx_websocket_close_total" {
				continue
			}
			for _, m := range f.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "code" {
						if lp.GetValue() == "1011" {
							t.Fatalf("routine normal close escalated to StatusInternalError (1011)")
						}
					}
				}
				return true
			}
		}
		return false
	}, 2*time.Second, 20*time.Millisecond, "close metric was never recorded")
}

func TestHandle_RespectsMaxMessageBytes(t *testing.T) {
	reg := prometheus.NewRegistry()
	var handlerErr error
	var wg sync.WaitGroup
	wg.Add(1)

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, c *websocket.Conn) error {
			defer wg.Done()
			_, _, err := c.ReadMessage()
			handlerErr = err
			return nil
		}),
		websocket.WithMaxMessageBytes(64),
		websocket.WithMetrics(reg),
	)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close(coderws.StatusNormalClosure, "") }()

	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(strings.Repeat("x", 256))))

	_, _, readErr := conn.Read(ctx)
	require.Error(t, readErr, "client should see a close frame after the limit is exceeded")

	wg.Wait()
	require.Error(t, handlerErr)
}

func TestHandle_Subprotocols(t *testing.T) {
	reg := prometheus.NewRegistry()
	var negotiated string
	done := make(chan struct{}, 1)

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, c *websocket.Conn) error {
			negotiated = c.Subprotocol()
			done <- struct{}{}
			return nil
		}),
		websocket.WithSubprotocols("kit.v1", "kit.v2"),
		websocket.WithMetrics(reg),
	)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), &coderws.DialOptions{
		Subprotocols: []string{"kit.v2"},
	})
	require.NoError(t, err)
	defer func() { _ = conn.Close(coderws.StatusNormalClosure, "") }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not invoked")
	}
	assert.Equal(t, "kit.v2", negotiated)
}

func TestHandle_PanicsOnNilHandler(t *testing.T) {
	assert.Panics(t, func() {
		websocket.WithHandler(nil)
	})
}

func TestHandle_PanicsOnBadMaxBytes(t *testing.T) {
	assert.Panics(t, func() {
		websocket.WithMaxMessageBytes(0)
	})
}

func TestHandle_PanicsOnNilLogger(t *testing.T) {
	assert.Panics(t, func() {
		websocket.WithLogger(nil)
	})
}

func TestHandle_PanicsOnNilMetricsReg(t *testing.T) {
	assert.Panics(t, func() {
		websocket.WithMetrics(nil)
	})
}

// TestHandle_PanicsOnPongTimeoutWithoutPingInterval guards the fail-fast
// contract: a pong timeout is inert without a heartbeat, so configuring
// it without WithPingInterval must surface as a startup panic rather
// than being silently dropped.
func TestHandle_PanicsOnPongTimeoutWithoutPingInterval(t *testing.T) {
	assert.Panics(t, func() {
		websocket.Handle(
			websocket.WithHandler(func(context.Context, *websocket.Conn) error { return nil }),
			websocket.WithPongTimeout(5*time.Second),
		)
	})
}

// TestHandle_PongTimeoutWithPingIntervalOK is the positive control: a
// pong timeout paired with a ping interval is a valid configuration and
// must not panic.
func TestHandle_PongTimeoutWithPingIntervalOK(t *testing.T) {
	assert.NotPanics(t, func() {
		websocket.Handle(
			websocket.WithHandler(func(context.Context, *websocket.Conn) error { return nil }),
			websocket.WithPingInterval(10*time.Second),
			websocket.WithPongTimeout(5*time.Second),
		)
	})
}

// TestHandle_Compression_Variants verifies both compression modes
// successfully negotiate a working WebSocket. Detailed permessage-
// deflate negotiation semantics belong in the upstream
// coder/websocket tests; here we are asserting only that the kit
// option plumbs through without breaking the upgrade.
func TestHandle_Compression_Variants(t *testing.T) {
	cases := []struct {
		name string
		opt  websocket.Option
	}{
		{"no-context-takeover", websocket.WithCompression()},
		{"context-takeover", websocket.WithCompressionContextTakeover()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reg := prometheus.NewRegistry()
			done := make(chan struct{}, 1)

			handler := websocket.Handle(
				websocket.WithHandler(func(_ context.Context, c *websocket.Conn) error {
					_, _, err := c.ReadMessage()
					done <- struct{}{}
					return err
				}),
				tc.opt,
				websocket.WithMetrics(reg),
			)

			srv := httptest.NewServer(handler)
			defer srv.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
			require.NoError(t, err)
			defer func() { _ = conn.Close(coderws.StatusNormalClosure, "") }()

			// A repetitive payload — compression's strong case — must
			// still round-trip end-to-end.
			require.NoError(t, conn.Write(ctx, coderws.MessageText,
				[]byte(strings.Repeat("kit", 1024))))

			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("handler did not read the message")
			}
		})
	}
}

// TestHandle_AnyOriginUnsafe_AcceptsCrossOrigin verifies that the
// unsafe-opt-in disables coder/websocket's same-origin enforcement.
// Without the option a request with a foreign Origin header would be
// rejected at upgrade.
func TestHandle_AnyOriginUnsafe_AcceptsCrossOrigin(t *testing.T) {
	reg := prometheus.NewRegistry()
	done := make(chan struct{}, 1)

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, _ *websocket.Conn) error {
			done <- struct{}{}
			return nil
		}),
		websocket.WithAnyOriginUnsafe(),
		websocket.WithMetrics(reg),
	)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"),
		&coderws.DialOptions{
			HTTPHeader: http.Header{"Origin": []string{"https://attacker.example"}},
		})
	require.NoError(t, err, "WithAnyOriginUnsafe must accept a foreign Origin")
	defer func() { _ = conn.Close(coderws.StatusNormalClosure, "") }()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler was not invoked")
	}
}

// TestHandle_DefaultRejectsCrossOrigin verifies the safe default: a
// foreign Origin must be rejected at upgrade without an explicit
// opt-in.
func TestHandle_DefaultRejectsCrossOrigin(t *testing.T) {
	reg := prometheus.NewRegistry()

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, _ *websocket.Conn) error {
			return nil
		}),
		websocket.WithMetrics(reg),
	)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"),
		&coderws.DialOptions{
			HTTPHeader: http.Header{"Origin": []string{"https://attacker.example"}},
		})
	require.Error(t, err, "default same-origin policy must reject foreign Origin")
}

// TestHandle_MaxConnections_Rejects503 asserts the end-to-end
// capacity-rejection path: when a configured cap is reached the next
// upgrade is answered with 503 + Retry-After before any WebSocket
// allocation, the rejected counter is bumped, and capacity is
// returned when an existing conn closes.
func TestHandle_MaxConnections_Rejects503(t *testing.T) {
	reg := prometheus.NewRegistry()
	release := make(chan struct{})
	entered := make(chan struct{}, 1)

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, c *websocket.Conn) error {
			entered <- struct{}{}
			<-release
			_ = c.Close(websocket.StatusNormalClosure, "test done")
			return nil
		}),
		websocket.WithMaxConnections(1),
		websocket.WithMetrics(reg),
	)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First connection takes the only slot.
	conn1, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = conn1.Close(coderws.StatusNormalClosure, "") }()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start for the first connection")
	}

	// Second connection must be rejected at the HTTP layer with 503.
	// We use a plain HTTP GET (not a real Dial) so we can inspect the
	// raw response — Dial would surface this as an opaque error.
	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, "1", resp.Header.Get("Retry-After"))

	// Counter must be bumped with the bounded reason label.
	families, err := reg.Gather()
	require.NoError(t, err)
	var rejected float64
	for _, f := range families {
		if f.GetName() != "httpx_websocket_rejected_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "reason" && lp.GetValue() == "max_connections" {
					rejected = m.GetCounter().GetValue()
				}
			}
		}
	}
	assert.Equal(t, float64(1), rejected,
		"rejected_total{reason=max_connections} must be 1 after the over-cap upgrade")

	// Release the first conn, then verify capacity is freed.
	close(release)
	_ = conn1.Close(coderws.StatusNormalClosure, "freeing slot")

	// Poll briefly for slot release — the defer in the handler runs
	// after the goroutine returns, which races the next Dial.
	require.Eventually(t, func() bool {
		c, _, derr := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if derr != nil {
			return false
		}
		_ = c.Close(coderws.StatusNormalClosure, "")
		return true
	}, 2*time.Second, 20*time.Millisecond, "slot was never released")
}

// TestHandle_PushOnlyHeartbeat_KilledWithoutReadDrain documents the
// upstream constraint: coder/websocket's Ping waits for a Reader call
// to pump the pong, so a server-push handler that never reads is killed
// by its own heartbeat. This is the failure mode WithReadDrain exists
// to fix.
func TestHandle_PushOnlyHeartbeat_KilledWithoutReadDrain(t *testing.T) {
	reg := prometheus.NewRegistry()
	ended := make(chan struct{}, 1)

	handler := websocket.Handle(
		websocket.WithHandler(func(ctx context.Context, c *websocket.Conn) error {
			defer func() { ended <- struct{}{} }()
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
					if err := c.WriteMessage(websocket.MessageText, []byte("tick")); err != nil {
						return err
					}
				}
			}
		}),
		websocket.WithPingInterval(30*time.Millisecond),
		websocket.WithPongTimeout(30*time.Millisecond),
		websocket.WithMetrics(reg),
	)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close(coderws.StatusNormalClosure, "") }()

	// Client reads continuously so it auto-responds to pings.
	go func() {
		for {
			if _, _, e := conn.Read(ctx); e != nil {
				return
			}
		}
	}()

	// The heartbeat must time out and close the push-only connection.
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the push-only handler to be torn down by the heartbeat")
	}

	require.Eventually(t, func() bool {
		families, gerr := reg.Gather()
		if gerr != nil {
			return false
		}
		for _, f := range families {
			if f.GetName() != "httpx_websocket_pings_total" {
				continue
			}
			for _, m := range f.GetMetric() {
				for _, lp := range m.GetLabel() {
					if lp.GetName() == "result" && lp.GetValue() == "timeout" &&
						m.GetCounter().GetValue() >= 1 {
						return true
					}
				}
			}
		}
		return false
	}, 2*time.Second, 20*time.Millisecond, "ping timeout was never recorded")
}

// TestHandle_PushOnlyHeartbeat_SurvivesWithReadDrain asserts the fix:
// with WithReadDrain the kit pumps the read side internally so the
// heartbeat's pong is read, and a push-only handler stays alive across
// several ping intervals without a timeout-driven close.
func TestHandle_PushOnlyHeartbeat_SurvivesWithReadDrain(t *testing.T) {
	reg := prometheus.NewRegistry()
	writes := make(chan struct{}, 64)

	handler := websocket.Handle(
		websocket.WithHandler(func(ctx context.Context, c *websocket.Conn) error {
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-ticker.C:
					if err := c.WriteMessage(websocket.MessageText, []byte("tick")); err != nil {
						return err
					}
					select {
					case writes <- struct{}{}:
					default:
					}
				}
			}
		}),
		websocket.WithReadDrain(),
		websocket.WithPingInterval(30*time.Millisecond),
		websocket.WithPongTimeout(30*time.Millisecond),
		websocket.WithMetrics(reg),
	)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)

	// Client reads continuously so it auto-responds to pings.
	go func() {
		for {
			if _, _, e := conn.Read(ctx); e != nil {
				return
			}
		}
	}()

	// Run for well past several ping intervals; the connection must
	// survive — i.e. writes keep flowing and no ping timeout is logged.
	deadline := time.After(400 * time.Millisecond)
	count := 0
loop:
	for {
		select {
		case <-writes:
			count++
		case <-deadline:
			break loop
		case <-time.After(200 * time.Millisecond):
			t.Fatal("push-only writes stalled — connection was torn down despite WithReadDrain")
		}
	}
	assert.Greater(t, count, 5, "expected continuous push writes across multiple ping intervals")

	// No ping must have timed out.
	families, gerr := reg.Gather()
	require.NoError(t, gerr)
	for _, f := range families {
		if f.GetName() != "httpx_websocket_pings_total" {
			continue
		}
		for _, m := range f.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "result" && lp.GetValue() == "timeout" {
					t.Fatalf("ping timed out despite WithReadDrain (count=%v)", m.GetCounter().GetValue())
				}
			}
		}
	}

	_ = conn.Close(coderws.StatusNormalClosure, "done")
}

// Sanity check that the Handle return type composes naturally with
// stdlib http middleware (in particular, that it remains a
// http.HandlerFunc rather than a wrapper type).
func TestHandle_IsStdHandlerFunc(t *testing.T) {
	reg := prometheus.NewRegistry()
	h := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, _ *websocket.Conn) error { return nil }),
		websocket.WithMetrics(reg),
	)
	_ = http.HandlerFunc(h)
}

// TestHub_ShutdownCancelsOpenConnection pins review-09: WithoutCancel severs
// request-context cancellation, but Hub.Shutdown cancels tracked conns and
// unblocks handlers parked on conn.Context().Done().
func TestHub_ShutdownCancelsOpenConnection(t *testing.T) {
	handlerEntered := make(chan struct{})
	handlerDone := make(chan struct{})

	hub := websocket.NewHub(
		websocket.WithHandler(func(ctx context.Context, c *websocket.Conn) error {
			close(handlerEntered)
			<-c.Context().Done()
			close(handlerDone)
			return nil
		}),
		websocket.WithReadDrain(),
	)

	srv := httptest.NewServer(hub.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close(coderws.StatusNormalClosure, "") }()

	select {
	case <-handlerEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	require.NoError(t, hub.Shutdown(shutdownCtx))

	select {
	case <-handlerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not observe context cancel after Hub.Shutdown")
	}
}

func TestNewHub_PanicsWithoutHandler(t *testing.T) {
	assert.Panics(t, func() { websocket.NewHub() })
}
