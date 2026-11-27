package centrifuge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func originServer(t *testing.T, opts ...Option) string {
	t.Helper()
	node, err := NewNode(append([]Option{WithAnonymousConnectionsUnsafe()}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	if err := node.Underlying().Run(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = node.Underlying().Shutdown(context.Background()) })
	server := httptest.NewServer(node.WebsocketHandler())
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http")
}

// dialStatus is the HTTP status of a WebSocket upgrade with origin.
func dialStatus(t *testing.T, url, origin string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	header := http.Header{}
	if origin != "" {
		header.Set("Origin", origin)
	}
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err == nil {
		_ = conn.CloseNow()
	}
	if resp == nil {
		t.Fatalf("dial %q: no response: %v", origin, err)
	}
	return resp.StatusCode
}

func TestWithAllowedOriginsAcceptsOnlyListedOrigins(t *testing.T) {
	t.Parallel()
	url := originServer(t, WithAllowedOrigins("https://app.example.com", "http://localhost:5173"))
	cases := map[string]int{
		"https://app.example.com":      http.StatusSwitchingProtocols,
		"HTTPS://App.Example.com":      http.StatusSwitchingProtocols,
		"http://localhost:5173":        http.StatusSwitchingProtocols,
		"https://evil.example.com":     http.StatusForbidden,
		"http://app.example.com":       http.StatusForbidden,
		"https://app.example.com:8443": http.StatusForbidden,
		"null":                         http.StatusForbidden,
		"":                             http.StatusForbidden,
	}
	for origin, want := range cases {
		if got := dialStatus(t, url, origin); got != want {
			t.Errorf("Origin %q: status %d, want %d", origin, got, want)
		}
	}
}

func TestWithoutAllowedOriginsKeepsSameHostCheck(t *testing.T) {
	t.Parallel()
	url := originServer(t)
	if got := dialStatus(t, url, "https://app.example.com"); got != http.StatusForbidden {
		t.Fatalf("foreign origin: status %d, want 403", got)
	}
	host := "http://" + strings.TrimPrefix(url, "ws://")
	if got := dialStatus(t, url, host); got != http.StatusSwitchingProtocols {
		t.Fatalf("same host: status %d, want 101", got)
	}
}

func TestWithAllowedOriginsRejectsInvalidEntries(t *testing.T) {
	t.Parallel()
	for _, bad := range [][]string{
		nil,
		{""},
		{"app.example.com"},
		{"ftp://app.example.com"},
		{"https://app.example.com/"},
		{"https://user@app.example.com"},
		{"https://app.example.com?x=1"},
		{"null"},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("WithAllowedOrigins(%q) did not panic", bad)
				}
			}()
			WithAllowedOrigins(bad...)
		}()
	}
}

func TestCanonicalOriginNormalisesDefaultPortsAndIPv6(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://app.example.com:443": "https://app.example.com",
		"http://app.example.com:80":   "http://app.example.com",
		"https://app.example.com:80":  "https://app.example.com:80",
		"http://app.example.com:443":  "http://app.example.com:443",
		"https://[2001:DB8::1]":       "https://[2001:db8::1]",
		"https://[2001:db8::1]:443":   "https://[2001:db8::1]",
		"https://[2001:db8::1]:8443":  "https://[2001:db8::1]:8443",
	}
	for in, want := range cases {
		got, ok := canonicalOrigin(in)
		if !ok || got != want {
			t.Errorf("canonicalOrigin(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestWithAllowedOriginsMatchesDefaultPortsAndIPv6(t *testing.T) {
	t.Parallel()
	url := originServer(t, WithAllowedOrigins("https://app.example.com:443", "https://[2001:db8::1]"))
	for origin, want := range map[string]int{
		"https://app.example.com":    http.StatusSwitchingProtocols,
		"https://[2001:db8::1]:443":  http.StatusSwitchingProtocols,
		"https://[2001:db8::2]":      http.StatusForbidden,
		"https://[2001:db8::1]:8443": http.StatusForbidden,
	} {
		if got := dialStatus(t, url, origin); got != want {
			t.Errorf("Origin %q: status %d, want %d", origin, got, want)
		}
	}
}
