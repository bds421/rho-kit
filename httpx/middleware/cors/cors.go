package cors

import (
	"fmt"
	"net/http"

	jcors "github.com/jub0bs/cors"
)

// Options configures Cross-Origin Resource Sharing headers.
type Options struct {
	// AllowedOrigins is the list of allowed origins. Use ["*"] to allow all.
	// Supports subdomain wildcards (e.g., "https://*.example.com").
	AllowedOrigins []string

	// AllowedMethods is the list of allowed HTTP methods.
	// Defaults to ["GET", "POST", "PUT", "DELETE", "OPTIONS"].
	AllowedMethods []string

	// AllowedHeaders is the list of allowed request headers.
	// Defaults to ["Content-Type", "Authorization"].
	AllowedHeaders []string

	// ExposedHeaders is the list of response headers that the browser is
	// allowed to access via JavaScript (Access-Control-Expose-Headers).
	// By default, only CORS-safelisted headers are exposed.
	ExposedHeaders []string

	// MaxAge is the max age in seconds for preflight cache.
	// Defaults to 86400 (24 hours).
	MaxAge int

	// AllowCredentials sets the Access-Control-Allow-Credentials header
	// when true. This allows browsers to send cookies and auth headers
	// in cross-origin requests. Only applies when AllowedOrigins is not ["*"]
	// (the CORS spec forbids credentials with wildcard origins).
	AllowCredentials bool
}

// applyDefaults sets zero-valued fields to production-safe defaults.
func (o *Options) applyDefaults() {
	if len(o.AllowedMethods) == 0 {
		o.AllowedMethods = []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}
	}
	if len(o.AllowedHeaders) == 0 {
		o.AllowedHeaders = []string{"Content-Type", "Authorization"}
	}
	if o.MaxAge == 0 {
		o.MaxAge = 86400
	}
}

// New returns middleware that adds CORS headers to responses.
//
// Panics if the configuration is invalid (e.g., AllowCredentials with
// wildcard origins, malformed origin patterns). This is intentional:
// CORS misconfiguration is a programming error that should be caught at
// startup, not at request time.
//
// Delegates to [github.com/jub0bs/cors] for Fetch-standard-compliant
// CORS handling, including preflight caching, origin pattern matching,
// and exposed-header support.
func New(opts Options) func(http.Handler) http.Handler {
	opts.applyDefaults()

	cfg := jcors.Config{
		Origins:         opts.AllowedOrigins,
		Credentialed:    opts.AllowCredentials,
		Methods:         opts.AllowedMethods,
		RequestHeaders:  opts.AllowedHeaders,
		ResponseHeaders: opts.ExposedHeaders,
		MaxAgeInSeconds: opts.MaxAge,
	}

	mw, err := jcors.NewMiddleware(cfg)
	if err != nil {
		panic(fmt.Sprintf("cors: invalid configuration: %v", err))
	}

	return func(next http.Handler) http.Handler {
		return mw.Wrap(next)
	}
}
