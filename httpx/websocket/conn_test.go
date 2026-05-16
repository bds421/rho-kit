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
