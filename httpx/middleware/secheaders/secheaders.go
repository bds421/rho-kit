package secheaders

import "net/http"

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
}

// WithFrameOption sets the X-Frame-Options value.
// Default: [Deny]. Use [SameOrigin] for services that need iframe embedding.
func WithFrameOption(opt FrameOption) Option {
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
	return func(c *config) { c.referrerPolicy = policy }
}

// WithPermissionsPolicy sets the Permissions-Policy header.
// Default: "geolocation=(), microphone=(), camera=()".
// Set to empty string to disable.
func WithPermissionsPolicy(policy string) Option {
	return func(c *config) { c.permissionsPolicy = policy }
}

// WithHSTS sets the Strict-Transport-Security header.
// Default: "max-age=63072000; includeSubDomains" (2 years).
// Set to empty string to disable (e.g., local dev without TLS).
func WithHSTS(value string) Option {
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
	return func(c *config) { c.cacheControl = value }
}

// WithContentSecurityPolicy sets the Content-Security-Policy header.
// Default: "default-src 'none'" (strictest — blocks all content loading).
// Override for services that serve HTML or need specific directives.
func WithContentSecurityPolicy(policy string) Option {
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
			if cfg.hsts != "" && r.TLS != nil {
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
