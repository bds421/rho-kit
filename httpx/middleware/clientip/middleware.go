package clientip

import (
	"net"
	"net/http"
)

// Middleware sets each request's RemoteAddr to the client address
// [ClientIPWithTrustedProxies] finds behind the trusted proxies, keeping
// the peer's port, so every handler and middleware that keys on
// RemoteAddr (rate limits, audit, logs) attributes the request to the
// client, not the proxy. A request from an untrusted peer keeps its
// RemoteAddr.
func Middleware(trusted []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			client := ClientIPWithTrustedProxies(r, trusted)
			if client != "" && client != remoteIPString(r.RemoteAddr) {
				_, port, err := net.SplitHostPort(r.RemoteAddr)
				if err != nil {
					port = "0"
				}
				r = r.Clone(r.Context())
				r.RemoteAddr = net.JoinHostPort(client, port)
			}
			next.ServeHTTP(w, r)
		})
	}
}
