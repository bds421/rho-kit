//go:build integration

package websocket_test

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/httpx/websocket/v2"
)

// TestEchoOverRealListener exercises the full round trip — dial, send
// N messages, receive N echoes, close — over a real TCP listener.
// This is the smoke test that catches regressions in the upgrade
// handshake, read loop, and close handling that pure httptest does
// not (httptest still goes through the stdlib server but uses an
// in-memory pipe-style conn for some platforms).
func TestEchoOverRealListener(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, c *websocket.Conn) error {
			for {
				typ, payload, err := c.ReadMessage()
				if err != nil {
					return nil
				}
				if err := c.WriteMessage(typ, payload); err != nil {
					return nil
				}
			}
		}),
	)

	mux := http.NewServeMux()
	mux.Handle("/ws", handler)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Serve(listener)
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		wg.Wait()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	url := "ws://" + listener.Addr().String() + "/ws"
	conn, _, err := coderws.Dial(ctx, url, nil)
	require.NoError(t, err)
	defer func() { _ = conn.Close(coderws.StatusNormalClosure, "") }()

	for _, msg := range []string{"alice", "bob", "carol"} {
		require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(msg)))
		typ, payload, err := conn.Read(ctx)
		require.NoError(t, err)
		assert.Equal(t, coderws.MessageText, typ)
		assert.Equal(t, msg, string(payload))
	}

	require.NoError(t, conn.Close(coderws.StatusNormalClosure, "done"))
}

// TestUpgradeFails_OnHTTPNonGet ensures that a non-WebSocket request
// to the same route produces an HTTP error (not a hang, not a panic)
// when the upgrade handshake fails.
func TestUpgradeFails_OnHTTPNonGet(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = listener.Close() }()

	handler := websocket.Handle(
		websocket.WithHandler(func(_ context.Context, c *websocket.Conn) error {
			return c.Close(websocket.StatusNormalClosure, "should not run")
		}),
	)

	mux := http.NewServeMux()
	mux.Handle("/ws", handler)

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Serve(listener)
	}()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		wg.Wait()
	}()

	url := "http://" + listener.Addr().String() + "/ws"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.True(t, resp.StatusCode >= 400, "expected HTTP error, got %d", resp.StatusCode)
}
