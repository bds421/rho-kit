package clientip_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bds421/rho-kit/httpx/v2/middleware/clientip"
)

// Behind a trusted proxy the request carries the client's address with the
// peer's port; from anyone else it keeps the peer's. With no trusted proxies
// nothing is rewritten, loopback included.
func TestMiddlewareRewritesRemoteAddrBehindTrustedProxies(t *testing.T) {
	var seen string
	h := clientip.Middleware(clientip.ParseTrustedProxies([]string{"10.0.0.0/8"}))(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = r.RemoteAddr
	}))
	for _, c := range []struct{ peer, forwarded, want string }{
		{"10.1.2.3:4567", "203.0.113.9", "203.0.113.9:4567"},
		{"10.1.2.3:4567", "2001:db8::1", "[2001:db8::1]:4567"},
		{"192.0.2.1:4567", "203.0.113.9", "192.0.2.1:4567"},
		{"10.1.2.3:4567", "", "10.1.2.3:4567"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = c.peer
		if c.forwarded != "" {
			r.Header.Set("X-Forwarded-For", c.forwarded)
		}
		h.ServeHTTP(httptest.NewRecorder(), r)
		if seen != c.want {
			t.Errorf("peer %s forwarded %q: RemoteAddr %q, want %q", c.peer, c.forwarded, seen, c.want)
		}
	}
	none := clientip.Middleware(nil)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r.RemoteAddr }))
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "127.0.0.1:4567"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	none.ServeHTTP(httptest.NewRecorder(), r)
	if seen != "127.0.0.1:4567" {
		t.Errorf("with no trusted proxies RemoteAddr %q", seen)
	}
}
