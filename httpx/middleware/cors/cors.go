package cors

import (
	"net/http"
	"strings"
	"unicode/utf8"

	jcors "github.com/jub0bs/cors"
	"golang.org/x/net/http/httpguts"

	"github.com/bds421/rho-kit/httpx/v2"
)

// Options configures Cross-Origin Resource Sharing headers.
type Options struct {
	// AllowedOrigins is the required list of allowed origins. Use ["*"] to
	// explicitly allow all origins. Leave this empty by not installing the
	// middleware when a service does not expose a browser CORS API.
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
	opts.detach()
	validateOptions(opts)

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
		panic("cors: invalid configuration")
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

func (o *Options) detach() {
	o.AllowedOrigins = cloneStrings(o.AllowedOrigins)
	o.AllowedMethods = cloneStrings(o.AllowedMethods)
	o.AllowedHeaders = cloneStrings(o.AllowedHeaders)
	o.ExposedHeaders = cloneStrings(o.ExposedHeaders)
}

func cloneStrings(in []string) []string {
	if in == nil {
		return nil
	}
	return append([]string(nil), in...)
}

func validateOptions(opts Options) {
	nonEmptyOrigin := false
	for _, origin := range opts.AllowedOrigins {
		if strings.TrimSpace(origin) != "" {
			nonEmptyOrigin = true
			break
		}
	}
	if !nonEmptyOrigin {
		panic("cors: AllowedOrigins must contain at least one origin or \"*\"")
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
