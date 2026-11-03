package websocket_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/httpx/websocket/v2"
)

// One member stops at its limit, an address at its own, and a refused
// upgrade holds no place in any limit.
func TestKeyLimits_PerMemberAndPerAddress(t *testing.T) {
	member := websocket.NewKeyLimit(2, func(r *http.Request) string { return r.URL.Query().Get("user") })
	address := websocket.NewKeyLimit(3, func(*http.Request) string { return "one-address" })
	handler := websocket.Handle(
		websocket.WithHandler(func(ctx context.Context, _ *websocket.Conn) error {
			<-ctx.Done()
			return nil
		}),
		websocket.WithKeyLimits(member, address),
		websocket.WithNoHeartbeat(),
		websocket.WithReadDrain(),
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()
	defer func() { _ = websocket.Shutdown(context.Background()) }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	open := func(user string) int {
		conn, resp, err := coderws.Dial(ctx, url+"?user="+user, nil)
		if err != nil {
			require.NotNil(t, resp)
			return resp.StatusCode
		}
		t.Cleanup(func() { _ = conn.CloseNow() })
		return http.StatusSwitchingProtocols
	}
	assert.Equal(t, http.StatusSwitchingProtocols, open("u1"))
	assert.Equal(t, http.StatusSwitchingProtocols, open("u1"))
	assert.Equal(t, http.StatusTooManyRequests, open("u1"), "u1 is at its member limit")
	assert.False(t, member.Room(httptest.NewRequest(http.MethodGet, "/?user=u1", nil)))
	assert.True(t, member.Room(httptest.NewRequest(http.MethodGet, "/?user=u2", nil)))
	assert.Equal(t, http.StatusSwitchingProtocols, open("u2"), "u1's refusal took no place of the address")
	assert.Equal(t, http.StatusTooManyRequests, open(""), "the address is at its limit")
	assert.False(t, address.Room(httptest.NewRequest(http.MethodGet, "/", nil)))
}

// A closed connection gives its places back.
func TestKeyLimits_ReleaseOnClose(t *testing.T) {
	limit := websocket.NewKeyLimit(1, func(*http.Request) string { return "k" })
	handler := websocket.Handle(
		websocket.WithHandler(func(ctx context.Context, _ *websocket.Conn) error {
			<-ctx.Done()
			return nil
		}),
		websocket.WithKeyLimits(limit),
		websocket.WithNoHeartbeat(),
		websocket.WithReadDrain(),
	)
	srv := httptest.NewServer(handler)
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	require.NoError(t, err)
	require.False(t, limit.Room(httptest.NewRequest(http.MethodGet, "/", nil)))
	require.NoError(t, conn.CloseNow())
	assert.Eventually(t, func() bool { return limit.Room(httptest.NewRequest(http.MethodGet, "/", nil)) }, 2*time.Second, 10*time.Millisecond)
}

func TestNewKeyLimit_RefusesMisconfiguration(t *testing.T) {
	assert.Panics(t, func() { websocket.NewKeyLimit(0, func(*http.Request) string { return "" }) })
	assert.Panics(t, func() { websocket.NewKeyLimit(1, nil) })
}
