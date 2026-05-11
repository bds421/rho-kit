// asvs: V9.2.1, V14.4.1
package secheaders

import (
	"net"
	"net/http"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// FrameOption controls the X-Frame-Options header value.
type FrameOption string

const (
	// Deny prevents the page from being displayed in any frame.
	Deny FrameOption = "DENY"

	// SameOrigin allows framing only by pages on the same origin.
	SameOrigin FrameOption = "SAMEORIGIN"
)

// Option configures the security headers middleware.
type Option func(*config)

type config struct {
	frameOption       FrameOption
	contentTypeOpt    bool
	referrerPolicy    string
	permissionsPolicy string
	hsts              string
	cacheControl      string
	csp               string
	trustedProxies    []*net.IPNet // for X-Forwarded-Proto trust
	forceHSTS         bool
}

// WithFrameOption sets the X-Frame-Options value.
// Default: [Deny]. Use [SameOrigin] for services that need iframe embedding.
func WithFrameOption(opt FrameOption) Option {
	if opt != "" && opt != Deny && opt != SameOrigin {
		panic("secheaders: invalid X-Frame-Options value")
	}
	return func(c *config) { c.frameOption = opt }
}

// WithoutContentTypeNoSniff disables the X-Content-Type-Options header.
func WithoutContentTypeNoSniff() Option {
	return func(c *config) { c.contentTypeOpt = false }
}

// WithReferrerPolicy sets the Referrer-Policy header.
// Default: "strict-origin-when-cross-origin".
// Set to empty string to disable.
func WithReferrerPolicy(policy string) Option {
	validateHeaderValue("Referrer-Policy", policy)
	return func(c *config) { c.referrerPolicy = policy }
}

// WithPermissionsPolicy sets the Permissions-Policy header.
// Default: "geolocation=(), microphone=(), camera=()".
// Set to empty string to disable.
func WithPermissionsPolicy(policy string) Option {
	validateHeaderValue("Permissions-Policy", policy)
	return func(c *config) { c.permissionsPolicy = policy }
}

// WithHSTS sets the Strict-Transport-Security header.
// Default: "max-age=63072000; includeSubDomains" (2 years).
// Set to empty string to disable (e.g., local dev without TLS).
func WithHSTS(value string) Option {
	validateHeaderValue("Strict-Transport-Security", value)
	return func(c *config) { c.hsts = value }
}

// WithoutHSTS disables the Strict-Transport-Security header.
// Use in development environments where TLS is not configured.
func WithoutHSTS() Option {
	return func(c *config) { c.hsts = "" }
}

// WithCacheControl sets the Cache-Control header for API responses.
// Default: "no-store". Set to empty string to disable (let handlers set it).
func WithCacheControl(value string) Option {
	validateHeaderValue("Cache-Control", value)
	return func(c *config) { c.cacheControl = value }
}

// WithTrustedProxiesForProto enables HSTS to fire when the request arrived
// over TLS at a trusted proxy (X-Forwarded-Proto: https) even if the
// connection from the proxy to this service is plaintext. The default
// `r.TLS != nil` check fails behind common TLS-terminating ingress
// topologies, so without this option HSTS is silently disabled for the
// majority of production deployments.
//
// Pass the same proxy CIDRs you supplied to clientip / ratelimit so all
// trust decisions agree. Pass nil or an empty slice to revert to the
// strict r.TLS-only check. Nil entries panic.
func WithTrustedProxiesForProto(proxies []*net.IPNet) Option {
	cleaned := make([]*net.IPNet, 0, len(proxies))
	for _, p := range proxies {
		if p == nil {
			panic("secheaders: trusted proxy CIDR must not be nil")
		}
		cleaned = append(cleaned, cloneIPNet(p))
	}
	return func(c *config) { c.trustedProxies = cloneIPNets(cleaned) }
}

// WithForceHSTS enables HSTS unconditionally on every response, regardless
// of whether r.TLS is set or X-Forwarded-Proto is honoured. Use this when
// the kit cannot observe the TLS state at all (custom listeners, unusual
// ingress topologies). Combine with care — sending HSTS over a plaintext
// origin is harmless but pointless.
func WithForceHSTS() Option {
	return func(c *config) { c.forceHSTS = true }
}

// WithContentSecurityPolicy sets the Content-Security-Policy header.
// Default: "default-src 'none'" (strictest — blocks all content loading).
// Override for services that serve HTML or need specific directives.
func WithContentSecurityPolicy(policy string) Option {
	validateHeaderValue("Content-Security-Policy", policy)
	return func(c *config) { c.csp = policy }
}

// New returns middleware that sets security response headers.
//
// With no options, it sets:
//   - X-Content-Type-Options: nosniff
//   - X-Frame-Options: DENY
//   - Referrer-Policy: strict-origin-when-cross-origin
//   - Permissions-Policy: geolocation=(), microphone=(), camera=()
//   - Strict-Transport-Security: max-age=63072000; includeSubDomains
//   - Cache-Control: no-store
//   - Content-Security-Policy: default-src 'none'
//
// HSTS is only sent when the request arrived over TLS (r.TLS != nil),
// per RFC 6797 §7.2. No configuration needed for mixed environments.
// For services that serve HTML, override [WithContentSecurityPolicy] and
// [WithCacheControl] as needed.
func New(opts ...Option) func(http.Handler) http.Handler {
	cfg := config{
		frameOption:       Deny,
		contentTypeOpt:    true,
		referrerPolicy:    "strict-origin-when-cross-origin",
		permissionsPolicy: "geolocation=(), microphone=(), camera=()",
		hsts:              "max-age=63072000; includeSubDomains",
		cacheControl:      "no-store",
		csp:               "default-src 'none'",
	}
	for _, opt := range opts {
		if opt == nil {
			panic("secheaders: option must not be nil")
		}
		opt(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			if cfg.contentTypeOpt {
				h.Set("X-Content-Type-Options", "nosniff")
			}
			if cfg.frameOption != "" {
				h.Set("X-Frame-Options", string(cfg.frameOption))
			}
			if cfg.referrerPolicy != "" {
				h.Set("Referrer-Policy", cfg.referrerPolicy)
			}
			if cfg.permissionsPolicy != "" {
				h.Set("Permissions-Policy", cfg.permissionsPolicy)
			}
			if cfg.hsts != "" && shouldSetHSTS(r, &cfg) {
				h.Set("Strict-Transport-Security", cfg.hsts)
			}
			if cfg.cacheControl != "" {
				h.Set("Cache-Control", cfg.cacheControl)
			}
			if cfg.csp != "" {
				h.Set("Content-Security-Policy", cfg.csp)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// shouldSetHSTS reports whether HSTS should be sent for this request. The
// default behaviour (r.TLS != nil) is the strict reading of RFC 6797 §7.2,
// but it fails in common Kubernetes ingress topologies where TLS terminates
// before reaching the service. WithTrustedProxiesForProto and WithForceHSTS
// expand that surface deliberately.
func shouldSetHSTS(r *http.Request, cfg *config) bool {
	if cfg.forceHSTS {
		return true
	}
	if r.TLS != nil {
		return true
	}
	if len(cfg.trustedProxies) == 0 {
		return false
	}
	if !isTrustedRemote(r.RemoteAddr, cfg.trustedProxies) {
		return false
	}
	return forwardedProtoHTTPS(r.Header)
}

func forwardedProtoHTTPS(h http.Header) bool {
	values := h.Values("X-Forwarded-Proto")
	if len(values) != 1 {
		return false
	}
	value := values[0]
	if strings.TrimSpace(value) == "" || !httpguts.ValidHeaderFieldValue(value) {
		return false
	}
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case 0, '\r', '\n':
			return false
		}
	}
	return strings.EqualFold(strings.TrimSpace(value), "https")
}

func isTrustedRemote(remoteAddr string, trusted []*net.IPNet) bool {
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, cidr := range trusted {
		if cidr == nil {
			continue
		}
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

func validateHeaderValue(name, value string) {
	if value != "" && strings.TrimSpace(value) != value {
		panic("secheaders: header value contains leading or trailing whitespace")
	}
	if !httpguts.ValidHeaderFieldValue(value) {
		panic("secheaders: header value is invalid")
	}
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case 0, '\r', '\n':
			panic("secheaders: header value is invalid")
		}
	}
}

func cloneIPNet(n *net.IPNet) *net.IPNet {
	return &net.IPNet{
		IP:   append(net.IP(nil), n.IP...),
		Mask: append(net.IPMask(nil), n.Mask...),
	}
}

func cloneIPNets(in []*net.IPNet) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(in))
	for _, n := range in {
		out = append(out, cloneIPNet(n))
	}
	return out
}
