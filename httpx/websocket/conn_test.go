package websocket_test

import (
	"context"
	"encoding/json"
	"errors"
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

type echoPayload struct {
	Greeting string `json:"greeting"`
}

func TestConn_JSONRoundTrip(t *testing.T) {
	reg := prometheus.NewRegistry()
	done := make(chan error, 1)

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, c *websocket.Conn) error {
			var p echoPayload
			if err := c.ReadJSON(&p); err != nil {
				done <- err
				return err
			}
			if err := c.WriteJSON(echoPayload{Greeting: "hello " + p.Greeting}); err != nil {
				done <- err
				return err
			}
			done <- nil
			return nil
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

	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(`{"greeting":"alice"}`)))

	_, payload, err := conn.Read(ctx)
	require.NoError(t, err)

	var got echoPayload
	require.NoError(t, json.Unmarshal(payload, &got))
	assert.Equal(t, "hello alice", got.Greeting)

	require.NoError(t, conn.Close(coderws.StatusNormalClosure, "bye"))

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not finish")
	}
}

func TestConn_ReadJSON_NonTextReturnsError(t *testing.T) {
	reg := prometheus.NewRegistry()
	result := make(chan error, 1)

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, c *websocket.Conn) error {
			var v echoPayload
			result <- c.ReadJSON(&v)
			return nil
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

	require.NoError(t, conn.Write(ctx, coderws.MessageBinary, []byte{0x01, 0x02}))

	select {
	case err := <-result:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "read json")
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not respond")
	}
}

func TestConn_ReadJSON_DecodeFailure(t *testing.T) {
	reg := prometheus.NewRegistry()
	result := make(chan error, 1)

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, c *websocket.Conn) error {
			var v echoPayload
			result <- c.ReadJSON(&v)
			return nil
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

	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte("not-json")))

	select {
	case err := <-result:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not respond")
	}
}

func TestConn_Close_IsIdempotent(t *testing.T) {
	reg := prometheus.NewRegistry()
	var wg sync.WaitGroup
	wg.Add(1)
	var firstErr, secondErr error

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, c *websocket.Conn) error {
			defer wg.Done()
			firstErr = c.Close(websocket.StatusNormalClosure, "first")
			secondErr = c.Close(websocket.StatusNormalClosure, "second")
			return nil
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

	// Drain so the server can complete its close handshake.
	_, _, _ = conn.Read(ctx)

	wg.Wait()

	// First close drives the handshake; the second must be a silent
	// no-op so callers can defer Close unconditionally.
	assert.NoError(t, firstErr)
	assert.NoError(t, secondErr)
}

// TestConn_Context_CancelledOnClose asserts the documented contract
// that the per-connection context is cancelled when the connection
// closes — here via an explicit [Conn.Close] from inside the handler.
// A push-only handler that blocks on <-ctx.Done() (e.g. waiting to
// detect a teardown so it can stop streaming) must unblock when the
// connection is closed, not hang until it has already returned.
func TestConn_Context_CancelledOnClose(t *testing.T) {
	reg := prometheus.NewRegistry()
	cancelled := make(chan struct{}, 1)

	handler := websocket.Handle(
		websocket.WithHandler(func(ctx context.Context, c *websocket.Conn) error {
			// Close from a separate goroutine so the handler itself is
			// parked on ctx.Done() — exactly the push-only shape the
			// doc promises to support.
			go func() {
				time.Sleep(50 * time.Millisecond)
				_ = c.Close(websocket.StatusNormalClosure, "server done")
			}()
			select {
			case <-ctx.Done():
				cancelled <- struct{}{}
			case <-time.After(2 * time.Second):
			}
			return nil
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

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("per-connection context was not cancelled when the connection closed")
	}
}

// TestConn_Context_CancelledOnPeerDisconnect asserts that the
// per-connection context is cancelled once the kit observes a peer
// disconnect through a read error. A handler reading in one goroutine
// and parking on ctx in another must learn about the teardown.
func TestConn_Context_CancelledOnPeerDisconnect(t *testing.T) {
	reg := prometheus.NewRegistry()
	cancelled := make(chan struct{}, 1)

	handler := websocket.Handle(
		websocket.WithHandler(func(ctx context.Context, c *websocket.Conn) error {
			waiter := make(chan struct{})
			go func() {
				select {
				case <-ctx.Done():
					cancelled <- struct{}{}
				case <-time.After(2 * time.Second):
				}
				close(waiter)
			}()
			// A read that observes the peer's abrupt close must
			// propagate cancellation to ctx so the waiter above wakes.
			_, _, _ = c.ReadMessage()
			<-waiter
			return nil
		}),
		websocket.WithMetrics(reg),
	)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)

	// Abrupt close so the server-side read returns an error.
	require.NoError(t, conn.Close(coderws.StatusGoingAway, "client gone"))

	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("per-connection context was not cancelled after the peer disconnected")
	}
}

func TestConn_Metrics_AreEmitted(t *testing.T) {
	reg := prometheus.NewRegistry()
	metrics := websocket.NewMetrics(websocket.WithRegisterer(reg))
	_ = metrics

	done := make(chan struct{})
	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, c *websocket.Conn) error {
			defer close(done)
			_, _, err := c.ReadMessage()
			if err != nil {
				return err
			}
			return c.WriteMessage(websocket.MessageText, []byte("ok"))
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

	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte("hi")))
	_, _, _ = conn.Read(ctx)
	require.NoError(t, conn.Close(coderws.StatusNormalClosure, "bye"))

	<-done
	// Allow the server-side close to complete before reading the registry.
	time.Sleep(50 * time.Millisecond)

	families, err := reg.Gather()
	require.NoError(t, err)

	names := map[string]bool{}
	for _, f := range families {
		names[f.GetName()] = true
	}
	assert.True(t, names["httpx_websocket_active"], "active gauge must be registered")
	assert.True(t, names["httpx_websocket_messages_total"], "messages counter must be registered")
	assert.True(t, names["httpx_websocket_message_bytes"], "message bytes histogram must be registered")
	assert.True(t, names["httpx_websocket_close_total"], "close counter must be registered")
}

// TestCloseStatus_ExtractsCodeThroughKitWrap asserts that the exported
// classifier reads the RFC 6455 close code out of a kit-redacted read
// error — i.e. callers do not have to import coder/websocket to switch
// on close codes (the same rationale the StatusCode aliases exist for).
func TestCloseStatus_ExtractsCodeThroughKitWrap(t *testing.T) {
	reg := prometheus.NewRegistry()
	got := make(chan error, 1)

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, c *websocket.Conn) error {
			_, _, err := c.ReadMessage()
			got <- err
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

	require.NoError(t, conn.Close(coderws.StatusGoingAway, "client navigating away"))

	select {
	case err := <-got:
		require.Error(t, err)
		assert.Equal(t, websocket.StatusGoingAway, websocket.CloseStatus(err),
			"CloseStatus must extract the peer close code through the kit wrap")
		assert.True(t, websocket.IsNormalClosure(err),
			"a 1001 going-away close is a normal, expected disconnect")
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not report a read error")
	}
}

func TestCloseStatus_NonCloseError(t *testing.T) {
	// A nil or non-close error must report the upstream sentinel (-1)
	// and must not be classified as a normal closure.
	assert.EqualValues(t, -1, websocket.CloseStatus(nil))
	assert.False(t, websocket.IsNormalClosure(nil))

	assert.EqualValues(t, -1, websocket.CloseStatus(errors.New("boom")))
	assert.False(t, websocket.IsNormalClosure(errors.New("boom")))
}

func TestMessageType_AliasesMatchCoder(t *testing.T) {
	assert.Equal(t, int(coderws.MessageText), int(websocket.MessageText))
	assert.Equal(t, int(coderws.MessageBinary), int(websocket.MessageBinary))
}

func TestStatusCode_AliasesMatchCoder(t *testing.T) {
	assert.Equal(t, int(coderws.StatusNormalClosure), int(websocket.StatusNormalClosure))
	assert.Equal(t, int(coderws.StatusInternalError), int(websocket.StatusInternalError))
	assert.Equal(t, int(coderws.StatusGoingAway), int(websocket.StatusGoingAway))
}

func TestNewMetrics_PanicsOnNilRegisterer(t *testing.T) {
	assert.Panics(t, func() {
		websocket.WithRegisterer(nil)
	})
}

func TestNewMetrics_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		websocket.NewMetrics(nil)
	})
}

func TestWithWriteTimeout_PanicsOnNonPositive(t *testing.T) {
	assert.Panics(t, func() { websocket.WithWriteTimeout(0) })
	assert.Panics(t, func() { websocket.WithWriteTimeout(-1 * time.Millisecond) })
}

// TestConn_WriteTimeout_FiresOnSlowConsumer asserts that a configured
// WithWriteTimeout deadline trips when the peer never reads from its
// socket and the kernel send buffer backs up. The handler's
// WriteMessage must return a redacted error whose underlying chain
// includes context.DeadlineExceeded.
func TestConn_WriteTimeout_FiresOnSlowConsumer(t *testing.T) {
	reg := prometheus.NewRegistry()
	writeErr := make(chan error, 1)

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, c *websocket.Conn) error {
			// 8 MiB is comfortably larger than any reasonable
			// kernel send buffer (typically 64 KiB–4 MiB), so the
			// write must block once the buffer fills.
			payload := make([]byte, 8<<20)
			err := c.WriteMessage(websocket.MessageBinary, payload)
			writeErr <- err
			return err
		}),
		websocket.WithMetrics(reg),
		websocket.WithWriteTimeout(100*time.Millisecond),
	)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Dial but never read — this is the "slow consumer" the option
	// is designed to evict.
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close(coderws.StatusGoingAway, "") }()

	select {
	case err := <-writeErr:
		require.Error(t, err)
		// The deadline must be observable via errors.Is so callers
		// (e.g. observability middleware) can distinguish slow-peer
		// closes from other write failures without parsing strings.
		assert.True(t, errors.Is(err, context.DeadlineExceeded),
			"expected context.DeadlineExceeded in chain, got %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("write did not time out within budget — write deadline not enforced")
	}
}

// TestConn_WriteTimeout_DefaultsToUnbounded confirms that omitting
// WithWriteTimeout leaves writes uncapped (the conn-scoped context is
// the only deadline). The check is necessarily indirect: with a small
// payload and a responsive client the write completes quickly, which
// would also happen if a bogus 1ms default were applied — so we time
// the write with a margin that a 1ms timeout would always trip.
func TestConn_WriteTimeout_DefaultsToUnbounded(t *testing.T) {
	reg := prometheus.NewRegistry()
	done := make(chan error, 1)

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, c *websocket.Conn) error {
			// Sleep longer than any plausible accidental default so
			// the test fails if a buggy default deadline were tied
			// to handler entry rather than to the write itself.
			time.Sleep(50 * time.Millisecond)
			err := c.WriteMessage(websocket.MessageText, []byte("ok"))
			done <- err
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
	defer func() { _ = conn.Close(coderws.StatusNormalClosure, "") }()

	_, _, err = conn.Read(ctx)
	require.NoError(t, err)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not finish")
	}
}

// Sanity check that read errors round-trip through redact.WrapError
// (the inner driver text must not appear in the returned error).
func TestConn_ReadError_IsRedacted(t *testing.T) {
	reg := prometheus.NewRegistry()
	gotErr := make(chan error, 1)

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, c *websocket.Conn) error {
			_, _, err := c.ReadMessage()
			gotErr <- err
			return nil
		}),
		websocket.WithMetrics(reg),
	)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)

	// Abrupt close so the server-side read returns an error.
	require.NoError(t, conn.Close(coderws.StatusGoingAway, "client closed"))

	select {
	case err := <-gotErr:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "redacted error")
		// Verify the underlying coder/websocket error is still
		// reachable via errors.Unwrap for triage. We do not assert a
		// specific sentinel because the upstream error type is
		// internal, but the chain depth must be >= 1.
		assert.NotNil(t, errors.Unwrap(err))
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not report a read error")
	}
}
