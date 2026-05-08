// Package tenant provides HTTP middleware that resolves the
// current request's tenant ID and stores it on the request context
// for downstream handlers (and other tenant-aware kit packages).
//
// The default extractor reads the "X-Tenant-Id" header. JWT-claim
// extraction is left to a small custom extractor at wire time so the
// httpx module doesn't pull a JWT dependency for callers who don't
// use it.
//
// When [WithRequired] is true (the default), every request without a
// tenant gets a 400 — including safe methods (GET/HEAD/OPTIONS).
// Health/readiness probes belong on a sibling router (e.g. the kit's
// internal ops port) so this middleware can stay strict.
// [WithAllowMissingTenantOnSafeMethods] is an explicit opt-out for
// services that intentionally expose pre-auth GETs through the same
// router (legacy compatibility).
//
// asvs: V4.1.1
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
	extractor                       Extractor
	required                        bool
	allowMissingTenantOnSafeMethods bool
}

// WithExtractor overrides the default header extractor.
func WithExtractor(e Extractor) Option {
	return func(c *config) { c.extractor = e }
}

// WithRequired controls whether a missing tenant returns 400. Default:
// true. When true, every request method is required to carry a tenant
// (including GET/HEAD/OPTIONS) — see
// [WithAllowMissingTenantOnSafeMethods] for the explicit opt-out.
func WithRequired(required bool) Option {
	return func(c *config) { c.required = required }
}

// WithAllowMissingTenantOnSafeMethods opts out of the default
// require-tenant-on-every-method rule for GET/HEAD/OPTIONS. Use this
// only when the same router intentionally serves pre-auth probes or
// discovery endpoints alongside tenant-scoped routes — the safer
// pattern is to mount those probes on a sibling router (the kit's
// internal ops port already does this for /health, /ready, /metrics).
//
// The default is OFF: [WithRequired] applies to every method. The
// previous behavior (safe-method short-circuit by default) let
// downstream tenant-budget enforcement be silently bypassed by GETs
// that omitted X-Tenant-Id. Making the bypass an explicit opt-out
// keeps that mistake from re-emerging.
func WithAllowMissingTenantOnSafeMethods() Option {
	return func(c *config) { c.allowMissingTenantOnSafeMethods = true }
}

// New returns the middleware. By default the tenant ID is read from
// the "X-Tenant-Id" header and required on every request — including
// GET/HEAD/OPTIONS. Mount health/readiness on a sibling router or use
// [WithAllowMissingTenantOnSafeMethods] when pre-auth GETs must share
// the public mux.
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
			if cfg.allowMissingTenantOnSafeMethods {
				switch r.Method {
				case http.MethodGet, http.MethodHead, http.MethodOptions:
					// Explicit opt-out: pre-auth GET/HEAD/OPTIONS
					// short-circuit through with no tenant on ctx.
					// Downstream tenant-keyed middleware (budget,
					// cache) MUST treat absent tenant as a bypass to
					// stay safe — see httpx/middleware/budget for the
					// reference behavior.
					next.ServeHTTP(w, r)
					return
				}
			}
			if cfg.required {
				httpx.WriteError(w, http.StatusBadRequest, "tenant: required tenant ID is missing")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
