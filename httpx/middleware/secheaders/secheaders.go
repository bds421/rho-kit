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
	coop              string       // Cross-Origin-Opener-Policy
	coep              string       // Cross-Origin-Embedder-Policy
	corp              string       // Cross-Origin-Resource-Policy
	trustedProxies    []*net.IPNet // for X-Forwarded-Proto trust
	forceHSTS         bool
}

// WithFrameOption sets the X-Frame-Options value.
// Default: [Deny]. Use [SameOrigin] for services that need iframe embedding.
func WithFrameOption(opt FrameOption) Option {
	if opt != "" && opt != Deny && opt != SameOrigin {
		panic("secheaders: WithFrameOption invalid X-Frame-Options value")
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
			panic("secheaders: WithTrustedProxiesForProto trusted proxy CIDR must not be nil")
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
// Default: "default-src 'none'; frame-ancestors 'none'" (strictest — blocks
// all content loading and clickjacking via iframe). Override for services
// that serve HTML or need specific directives.
func WithContentSecurityPolicy(policy string) Option {
	validateHeaderValue("Content-Security-Policy", policy)
	return func(c *config) { c.csp = policy }
}

// WithCrossOriginOpenerPolicy sets the Cross-Origin-Opener-Policy
// header value. Default: "same-origin" — isolates this origin's
// browsing-context group so cross-origin window references (the
// classic "tab-napping" / Spectre side-channel surface) are severed.
// Set to empty to suppress the header.
//
// Valid values: "unsafe-none", "same-origin-allow-popups", "same-origin".
// The middleware does not enforce the enum at runtime; consult
// MDN for the value matrix before deviating from the default.
func WithCrossOriginOpenerPolicy(value string) Option {
	validateHeaderValue("Cross-Origin-Opener-Policy", value)
	return func(c *config) { c.coop = value }
}

// WithoutCrossOriginOpener disables the Cross-Origin-Opener-Policy
// header. Use ONLY for services that intentionally rely on
// cross-origin window.opener access (legacy OAuth popup flows,
// embedded payment iframes) — the default isolates same-origin
// browsing contexts and breaking that isolation re-opens the
// Spectre / cross-origin window-reference attack surface.
func WithoutCrossOriginOpener() Option {
	return func(c *config) { c.coop = "" }
}

// WithCrossOriginEmbedderPolicy sets the Cross-Origin-Embedder-Policy
// header value. Default: "require-corp" — every cross-origin
// subresource (images, scripts, fonts) must explicitly opt in via a
// matching Cross-Origin-Resource-Policy / CORS response.
//
// COEP=require-corp is the gate for cross-origin isolation (the
// browser's SharedArrayBuffer / high-resolution timer API set). The
// trade-off is real: a single embedded resource lacking a CORP /
// CORS allowance fails to load. Audit every third-party JS SDK,
// font CDN, and analytics tag before enabling this in production —
// or opt out per-service via [WithoutCrossOriginEmbedder].
//
// Valid values: "unsafe-none", "require-corp", "credentialless".
func WithCrossOriginEmbedderPolicy(value string) Option {
	validateHeaderValue("Cross-Origin-Embedder-Policy", value)
	return func(c *config) { c.coep = value }
}

// WithoutCrossOriginEmbedder disables the Cross-Origin-Embedder-Policy
// header. The right opt-out for iframe-heavy services that cannot
// require every embedded resource to carry CORP — most marketing
// sites and admin dashboards with third-party widgets fall into
// this bucket.
func WithoutCrossOriginEmbedder() Option {
	return func(c *config) { c.coep = "" }
}

// WithCrossOriginResourcePolicy sets the Cross-Origin-Resource-Policy
// header value. Default: "same-origin" — this service's responses
// can only be loaded by same-origin documents, preventing
// cross-origin embedding (Spectre-style read primitives, hotlinking,
// resource hijack).
//
// Valid values: "same-site", "same-origin", "cross-origin".
func WithCrossOriginResourcePolicy(value string) Option {
	validateHeaderValue("Cross-Origin-Resource-Policy", value)
	return func(c *config) { c.corp = value }
}

// WithoutCrossOriginResource disables the Cross-Origin-Resource-Policy
// header. Use when responses must be embeddable cross-origin (CDN
// origins, public asset hosts).
func WithoutCrossOriginResource() Option {
	return func(c *config) { c.corp = "" }
}

// WithoutCrossOriginPolicies disables all three Cross-Origin-* policy
// headers at once. The right escape hatch for iframe-heavy services
// (legacy admin UIs, dashboards embedding third-party content) where
// the default isolation breaks the embed contract.
//
// Prefer the per-header opt-outs when only one policy needs to
// relax — turning all three off re-opens the Spectre / window-
// reference attack surface and should be a deliberate decision.
func WithoutCrossOriginPolicies() Option {
	return func(c *config) {
		c.coop = ""
		c.coep = ""
		c.corp = ""
	}
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
//   - Content-Security-Policy: default-src 'none'; frame-ancestors 'none'
//   - Cross-Origin-Opener-Policy: same-origin
//   - Cross-Origin-Embedder-Policy: require-corp
//   - Cross-Origin-Resource-Policy: same-origin
//
// HSTS is only sent when the request arrived over TLS (r.TLS != nil),
// per RFC 6797 §7.2. No configuration needed for mixed environments.
// For services that serve HTML, override [WithContentSecurityPolicy] and
// [WithCacheControl] as needed.
//
// The Cross-Origin-* trio (COOP/COEP/CORP) defaults to the
// cross-origin-isolation set. Services that legitimately rely on
// embedded third-party widgets, OAuth popup flows, or public asset
// distribution must opt out via [WithoutCrossOriginEmbedder] /
// [WithoutCrossOriginOpener] / [WithoutCrossOriginResource] (or
// disable all three with [WithoutCrossOriginPolicies]).
//
// Operators MUST audit their third-party SDKs and CDN dependencies
// before deploying with COEP=require-corp: every cross-origin
// subresource — JS bundles, fonts, analytics tags, image hosts —
// has to opt in via its own Cross-Origin-Resource-Policy or CORS
// response, or the resource fails to load.
func New(opts ...Option) func(http.Handler) http.Handler {
	cfg := config{
		frameOption:       Deny,
		contentTypeOpt:    true,
		referrerPolicy:    "strict-origin-when-cross-origin",
		permissionsPolicy: "geolocation=(), microphone=(), camera=()",
		hsts:              "max-age=63072000; includeSubDomains",
		cacheControl:      "no-store",
		csp:               "default-src 'none'; frame-ancestors 'none'",
		coop:              "same-origin",
		coep:              "require-corp",
		corp:              "same-origin",
	}
	for _, opt := range opts {
		if opt == nil {
			panic("secheaders: New option must not be nil")
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
			if cfg.coop != "" {
				h.Set("Cross-Origin-Opener-Policy", cfg.coop)
			}
			if cfg.coep != "" {
				h.Set("Cross-Origin-Embedder-Policy", cfg.coep)
			}
			if cfg.corp != "" {
				h.Set("Cross-Origin-Resource-Policy", cfg.corp)
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
		panic("secheaders: " + name + " header value contains leading or trailing whitespace")
	}
	if !httpguts.ValidHeaderFieldValue(value) {
		panic("secheaders: " + name + " header value is invalid")
	}
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case 0, '\r', '\n':
			panic("secheaders: " + name + " header value is invalid")
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
