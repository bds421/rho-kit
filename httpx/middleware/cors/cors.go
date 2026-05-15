package cors

import (
	"net/http"
	"strings"
	"unicode/utf8"

	jcors "github.com/jub0bs/cors"
	"golang.org/x/net/http/httpguts"

	"github.com/bds421/rho-kit/httpx/v2"
)

// Option configures the CORS middleware. Compose options in [New] to
// set allowed origins / methods / headers, preflight TTL, and the
// credentials flag.
type Option func(*config)

// config holds the resolved CORS settings. Kept package-private because
// the Option-shape is the public API — adopters compose with [WithAllowedOrigins]
// and friends rather than building structs by hand.
type config struct {
	allowedOrigins   []string
	allowedMethods   []string
	allowedHeaders   []string
	exposedHeaders   []string
	maxAge           int
	allowCredentials bool
}

// WithAllowedOrigins sets the list of allowed origins. At least one
// non-empty entry is required — use ["*"] to explicitly allow all
// origins. Supports subdomain wildcards (e.g., "https://*.example.com").
// The argument slice is defensively copied.
func WithAllowedOrigins(origins ...string) Option {
	captured := append([]string(nil), origins...)
	return func(c *config) { c.allowedOrigins = captured }
}

// WithAllowedMethods overrides the default allowed HTTP methods
// (GET, POST, PUT, DELETE, OPTIONS). The argument slice is defensively copied.
func WithAllowedMethods(methods ...string) Option {
	captured := append([]string(nil), methods...)
	return func(c *config) { c.allowedMethods = captured }
}

// WithAllowedHeaders overrides the default allowed request headers
// (Content-Type, Authorization). The argument slice is defensively copied.
func WithAllowedHeaders(headers ...string) Option {
	captured := append([]string(nil), headers...)
	return func(c *config) { c.allowedHeaders = captured }
}

// WithExposedHeaders sets the response headers the browser is allowed
// to access via JavaScript (Access-Control-Expose-Headers). Empty by
// default — browsers expose only CORS-safelisted response headers.
// The argument slice is defensively copied.
func WithExposedHeaders(headers ...string) Option {
	captured := append([]string(nil), headers...)
	return func(c *config) { c.exposedHeaders = captured }
}

// WithMaxAge overrides the preflight-cache TTL in seconds (default 86400).
// Must be >= 0.
func WithMaxAge(seconds int) Option {
	if seconds < 0 {
		panic("middleware/cors: WithMaxAge requires a non-negative duration")
	}
	return func(c *config) { c.maxAge = seconds }
}

// WithCredentials enables Access-Control-Allow-Credentials. Cannot be
// combined with `["*"]` origins — the CORS spec forbids credentialed
// requests against wildcard origins; the validator rejects that combo at
// New() time.
func WithCredentials() Option {
	return func(c *config) { c.allowCredentials = true }
}

// New returns middleware that adds CORS headers to responses.
//
// At minimum [WithAllowedOrigins] must be supplied — calling New() with
// no allowed origins panics, because a CORS middleware with no allowed
// origins blocks every cross-origin request and silently shadows the
// configuration mistake.
//
// Panics if the configuration is invalid (e.g., AllowCredentials with
// wildcard origins, malformed origin patterns). This is intentional:
// CORS misconfiguration is a programming error that should be caught at
// startup, not at request time.
//
// Delegates to [github.com/jub0bs/cors] for Fetch-standard-compliant
// CORS handling, including preflight caching, origin pattern matching,
// and exposed-header support.
func New(opts ...Option) func(http.Handler) http.Handler {
	cfg := config{
		allowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		allowedHeaders: []string{"Content-Type", "Authorization"},
		maxAge:         86400,
	}
	for _, o := range opts {
		if o == nil {
			panic("middleware/cors: New: option must not be nil")
		}
		o(&cfg)
	}
	validateConfig(cfg)

	jcfg := jcors.Config{
		Origins:         cfg.allowedOrigins,
		Credentialed:    cfg.allowCredentials,
		Methods:         cfg.allowedMethods,
		RequestHeaders:  cfg.allowedHeaders,
		ResponseHeaders: cfg.exposedHeaders,
		MaxAgeInSeconds: cfg.maxAge,
	}

	mw, err := jcors.NewMiddleware(jcfg)
	if err != nil {
		panic("middleware/cors: New: invalid configuration")
	}

	return func(next http.Handler) http.Handler {
		wrapped := mw.Wrap(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !validOptionalSingletonHeader(r.Header, "Origin") ||
				!validOptionalSingletonHeader(r.Header, "Access-Control-Request-Method") ||
				!validOptionalSingletonHeader(r.Header, "Access-Control-Request-Headers") {
				httpx.WriteError(w, http.StatusBadRequest, "invalid CORS request")
				return
			}
			wrapped.ServeHTTP(w, r)
		})
	}
}

func validateConfig(cfg config) {
	nonEmptyOrigin := false
	for _, origin := range cfg.allowedOrigins {
		if strings.TrimSpace(origin) != "" {
			nonEmptyOrigin = true
			break
		}
	}
	if !nonEmptyOrigin {
		panic("middleware/cors: WithAllowedOrigins must be supplied with at least one origin or \"*\"")
	}
}

func validOptionalSingletonHeader(h http.Header, name string) bool {
	values := h.Values(name)
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 {
		return false
	}
	value := values[0]
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || !httpguts.ValidHeaderFieldValue(value) {
		return false
	}
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case 0, '\r', '\n':
			return false
		}
	}
	return true
}
