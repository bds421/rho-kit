package centrifuge

import (
	"net"
	"net/http"
	"net/url"
	"strings"

	coreconfig "github.com/bds421/rho-kit/core/v2/config"
)

// WithAllowedOrigins makes [Node.WebsocketHandler] accept a WebSocket
// upgrade only when its Origin header is one of origins, compared as
// "scheme://host[:port]" without regard to case, a default port (443 for
// https, 80 for http) the same as none. Centrifuge's default
// compares the Origin with the request's Host, which behind a reverse
// proxy is the proxy's upstream name rather than the public one, so
// every browser is refused; this option names the public origins instead.
//
// A request without an Origin header, or with "null", is refused too:
// browsers always send it on a WebSocket upgrade, and a non-browser
// client can set it. Each origin must be http or https with a host and
// no user info, path, query or fragment; an invalid entry or an empty
// list panics at construction. Repeated options replace the list.
func WithAllowedOrigins(origins ...string) Option {
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		canonical, ok := canonicalOrigin(o)
		if !ok {
			panic("realtime/centrifuge: WithAllowedOrigins invalid origin")
		}
		allowed[canonical] = struct{}{}
	}
	if len(allowed) == 0 {
		panic("realtime/centrifuge: WithAllowedOrigins requires at least one origin")
	}
	return func(c *config) { c.allowedOrigins = allowed }
}

// originCheck returns the upgrade's origin check for allowed; nil keeps
// centrifuge's same-host default.
func originCheck(allowed map[string]struct{}) func(*http.Request) bool {
	if allowed == nil {
		return nil
	}
	return func(r *http.Request) bool {
		canonical, ok := canonicalOrigin(r.Header.Get("Origin"))
		if !ok {
			return false
		}
		_, ok = allowed[canonical]
		return ok
	}
}

// canonicalOrigin is raw as lower-case "scheme://host[:port]", or false
// when raw is not an http or https origin.
func canonicalOrigin(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	if u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", false
	}
	if coreconfig.ValidateURLHost("origin", u) != nil {
		return "", false
	}
	return scheme + "://" + hostWithoutDefaultPort(scheme, u), true
}

// hostWithoutDefaultPort is u's lower-case host, with the port only when
// it is not the scheme's default, so "https://example.com:443" and
// "https://example.com" are one origin.
func hostWithoutDefaultPort(scheme string, u *url.URL) string {
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		return net.JoinHostPort(host, port)
	}
	if strings.Contains(host, ":") {
		return "[" + host + "]"
	}
	return host
}
