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
