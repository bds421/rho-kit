// Package tenant provides HTTP middleware that resolves the
// current request's tenant ID and stores it on the request context
// for downstream handlers (and other tenant-aware kit packages).
//
// The default extractor reads the "X-Tenant-Id" header. JWT-claim
// extraction is left to a small custom extractor at wire time so the
// httpx module doesn't pull a JWT dependency for callers who don't
// use it.
//
// When [WithRequired] is true (the default), state-changing requests
// without a tenant get a 400. GET/HEAD/OPTIONS short-circuit through
// the middleware so health and discovery endpoints stay reachable
// pre-auth.
package tenant

import (
	"errors"
	"net/http"

	coretenant "github.com/bds421/rho-kit/core/tenant"
	"github.com/bds421/rho-kit/httpx"
)

// Extractor pulls a tenant ID from a request.
//
// Return semantics:
//   - (zero ID, nil)        — no tenant present (header missing / empty);
//     middleware applies the [WithRequired] rules.
//   - (validated ID, nil)   — tenant present and well-formed.
//   - (zero ID, non-nil err) — tenant present but invalid; middleware
//     responds with 400 and never invokes the next handler.
//
// Custom extractors (JWT claim, mTLS cert, query string, etc.) MUST
// validate via [coretenant.NewID] (or run [coretenant.ValidateID]
// themselves) before returning a non-nil ID. Returning an unvalidated
// raw value would let malformed tenant material reach downstream
// cache/idempotency/log/metric keys.
type Extractor func(*http.Request) (coretenant.ID, error)

// HeaderExtractor returns an Extractor that reads `header` from the
// request and validates the value through [coretenant.NewID]. Invalid
// values cause the middleware to respond with 400; missing/empty
// values are reported as absent and handled per [WithRequired].
func HeaderExtractor(header string) Extractor {
	if header == "" {
		panic("tenant: HeaderExtractor header must not be empty")
	}
	return func(r *http.Request) (coretenant.ID, error) {
		v := r.Header.Get(header)
		if v == "" {
			return "", nil
		}
		id, err := coretenant.NewID(v)
		if err != nil {
			return "", err
		}
		return id, nil
	}
}

// Option configures the middleware.
type Option func(*config)

type config struct {
	extractor             Extractor
	required              bool
	requiredOnSafeMethods bool
}

// WithExtractor overrides the default header extractor.
func WithExtractor(e Extractor) Option {
	return func(c *config) { c.extractor = e }
}

// WithRequired controls whether a missing tenant returns 400. Default:
// true.
func WithRequired(required bool) Option {
	return func(c *config) { c.required = required }
}

// WithRequiredOnSafeMethods controls whether GET/HEAD/OPTIONS requests
// without a tenant are also rejected when [WithRequired] is true.
//
// Default: false — preserving the existing behaviour where safe
// methods short-circuit through the middleware so health/readiness
// and discovery endpoints stay reachable when the tenant header has
// not been set yet.
//
// Set to true on routers that mount the tenant middleware in front of
// state-revealing GETs (per-tenant data lists, dashboards). Without
// this option a caller can issue GET /tenants/123/secrets without any
// X-Tenant-Id header at all, and the handler runs against an empty
// tenant context — the handler must compensate, which is easy to
// forget.
//
// Trade-off: enabling this requires the operator to expose health
// endpoints on a sibling router (or supply [WithRequired(false)]) so
// pre-auth probes do not 400.
func WithRequiredOnSafeMethods(required bool) Option {
	return func(c *config) { c.requiredOnSafeMethods = required }
}

// New returns the middleware. By default the tenant ID is read from
// the "X-Tenant-Id" header and required on every state-changing
// request.
func New(opts ...Option) func(http.Handler) http.Handler {
	cfg := config{
		extractor: HeaderExtractor("X-Tenant-Id"),
		required:  true,
	}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.extractor == nil {
		panic("tenant: extractor must not be nil")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id, err := cfg.extractor(r)
			if err != nil {
				// Present-but-invalid tenant ID. Reject with 400 so
				// downstream cache/idempotency/log/metric keys never
				// see malformed tenant material. Surface ErrInvalid
				// detail so operators get an actionable message.
				if errors.Is(err, coretenant.ErrInvalid) {
					httpx.WriteError(w, http.StatusBadRequest, "tenant: "+err.Error())
				} else {
					httpx.WriteError(w, http.StatusBadRequest, "tenant: invalid tenant ID")
				}
				return
			}
			if !id.IsZero() {
				r = r.WithContext(coretenant.WithID(r.Context(), id))
				next.ServeHTTP(w, r)
				return
			}
			isSafe := false
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				isSafe = true
			}
			if isSafe && !cfg.requiredOnSafeMethods {
				// Safe methods short-circuit through — they do not
				// mutate state and may be reachable pre-auth (health,
				// discovery). Opt in to enforcement via
				// WithRequiredOnSafeMethods when the route surfaces
				// tenant-scoped data on GET.
				next.ServeHTTP(w, r)
				return
			}
			if cfg.required {
				httpx.WriteError(w, http.StatusBadRequest, "tenant: required tenant ID is missing")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
