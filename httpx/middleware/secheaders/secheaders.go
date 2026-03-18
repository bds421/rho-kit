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
	frameOption    FrameOption
	contentTypeOpt bool
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

// New returns middleware that sets security response headers.
// With no options, it sets:
//   - X-Content-Type-Options: nosniff
//   - X-Frame-Options: DENY
func New(opts ...Option) func(http.Handler) http.Handler {
	cfg := config{
		frameOption:    Deny,
		contentTypeOpt: true,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.contentTypeOpt {
				w.Header().Set("X-Content-Type-Options", "nosniff")
			}
			if cfg.frameOption != "" {
				w.Header().Set("X-Frame-Options", string(cfg.frameOption))
			}
			next.ServeHTTP(w, r)
		})
	}
}
