// Package authz provides a pluggable authorization abstraction for HTTP services.
//
// Define a Policy that checks whether a subject can perform an action on a resource,
// then use RequirePermission as middleware to enforce authorization on routes.
//
//	policy := myapp.NewCasbinPolicy(enforcer)
//	mux.Handle("DELETE /users/{id}",
//	    authz.RequirePermission(policy, "delete",
//	        authz.ResourceFromPath("id"),
//	        authz.SubjectFromContext(userIDKey),
//	    )(deleteUserHandler),
//	)
package authz

import (
	"context"
	"log/slog"
	"net"
	"net/http"

	"github.com/bds421/rho-kit/httpx"
)

// Policy decides whether a subject may perform an action on a resource.
// Implementations may call external services (OPA, Casbin, database) — they
// must be safe for concurrent use.
type Policy interface {
	Allowed(ctx context.Context, subject, action, resource string) (bool, error)
}

// SubjectFunc extracts the subject (e.g. user ID) from the request.
type SubjectFunc func(r *http.Request) string

// ResourceFunc extracts the resource identifier from the request.
type ResourceFunc func(r *http.Request) string

// RequirePermission returns middleware that checks the policy before calling
// the next handler. Returns 403 on denial, 500 on policy error.
//
// Panics if policy, resource, or subject is nil — these are programming
// errors that would otherwise surface as a nil-deref on the first request,
// long after the misconfigured route has been mounted.
func RequirePermission(policy Policy, action string, resource ResourceFunc, subject SubjectFunc, opts ...MiddlewareOption) func(http.Handler) http.Handler {
	if policy == nil {
		panic("authz: RequirePermission requires a non-nil Policy")
	}
	if resource == nil {
		panic("authz: RequirePermission requires a non-nil ResourceFunc")
	}
	if subject == nil {
		panic("authz: RequirePermission requires a non-nil SubjectFunc")
	}
	cfg := middlewareConfig{
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sub := subject(r)
			if sub == "" {
				httpx.WriteError(w, http.StatusUnauthorized, "missing subject identity")
				return
			}
			res := resource(r)

			allowed, err := policy.Allowed(r.Context(), sub, action, res)
			if err != nil {
				cfg.logger.Error("authorization policy error",
					"error", err,
					"subject", sub,
					"action", action,
					"resource", res,
				)
				httpx.WriteError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if !allowed {
				cfg.logger.Warn("authorization denied",
					"subject", sub,
					"action", action,
					"resource", res,
				)
				httpx.WriteError(w, http.StatusForbidden, "forbidden")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// MiddlewareOption configures the authorization middleware.
type MiddlewareOption func(*middlewareConfig)

type middlewareConfig struct {
	logger *slog.Logger
}

// WithLogger sets the logger for authorization errors.
func WithLogger(l *slog.Logger) MiddlewareOption {
	return func(c *middlewareConfig) { c.logger = l }
}

// --- Extractor helpers ---

// ResourceFromPath returns a ResourceFunc that extracts a path parameter by name.
func ResourceFromPath(param string) ResourceFunc {
	return func(r *http.Request) string {
		return r.PathValue(param)
	}
}

// SubjectFromUntrustedHeader returns a SubjectFunc that reads a header value
// directly from the request without verifying that the request originated
// from a trusted reverse-proxy. Any caller able to reach the service can set
// the header to an arbitrary value, which makes this extractor unsafe for
// authorization decisions in production.
//
// Prefer [SubjectFromTrustedHeader] (which verifies r.RemoteAddr against a
// trusted-proxy CIDR list) or [SubjectFromContext] (which reads from
// authenticated middleware-populated context values).
//
// Use this only in tests and non-production fixtures where the only caller
// is the test harness itself.
func SubjectFromUntrustedHeader(header string) SubjectFunc {
	return func(r *http.Request) string {
		return r.Header.Get(header)
	}
}

// SubjectFromHeader returns a SubjectFunc that reads a header value.
//
// Deprecated: SubjectFromHeader trusts a request header that any client can
// spoof. Use [SubjectFromTrustedHeader] (with a trusted-proxy CIDR list) or
// [SubjectFromContext] (with an auth-middleware extractor) instead. This
// function is kept as a thin alias of [SubjectFromUntrustedHeader] and emits
// a WARN log on every construction so the misuse is visible in operator
// log streams (not just once per process — the previous sync.Once gate was
// too quiet, since operators reading logs would only ever see one entry
// regardless of how many spoof-able subject extractors the service had
// wired). Construct this function from a one-off init() and the warning
// fires once; construct it from a per-request hot path and the warning
// fires per-request.
func SubjectFromHeader(header string) SubjectFunc {
	slog.Warn("authz: SubjectFromHeader is deprecated and trusts a spoofable header; use SubjectFromTrustedHeader or SubjectFromContext",
		"header", header,
	)
	return SubjectFromUntrustedHeader(header)
}

// SubjectFromTrustedHeader returns a SubjectFunc that reads a header value
// only when r.RemoteAddr falls within the supplied trustedProxies CIDR list.
// When the request did not arrive via a trusted proxy, the extractor returns
// "" — which causes [RequirePermission] to reject the request with 401
// ("missing subject identity") via its existing fail-closed path.
//
// This prevents subject spoofing: a caller who connects directly (not through
// the configured ingress) can still set the header, but the middleware will
// ignore the value because the connection's source IP is outside the trusted
// proxy range.
//
// trustedProxies must list the CIDRs (or single-IP /32 / /128 entries) of
// the reverse proxies that legitimately set the header. A nil or empty list
// rejects every request because no remote can be trusted; use
// [SubjectFromContext] in deployments without a header-stamping proxy.
func SubjectFromTrustedHeader(header string, trustedProxies []*net.IPNet) SubjectFunc {
	return func(r *http.Request) string {
		if !remoteAddrInTrustedProxies(r.RemoteAddr, trustedProxies) {
			return ""
		}
		return r.Header.Get(header)
	}
}

// remoteAddrInTrustedProxies reports whether the host portion of remoteAddr
// (host:port) is contained in any of the supplied CIDRs.
func remoteAddrInTrustedProxies(remoteAddr string, trusted []*net.IPNet) bool {
	if len(trusted) == 0 {
		return false
	}
	host := remoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		host = h
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, cidr := range trusted {
		if cidr != nil && cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// SubjectFromContext returns a SubjectFunc that extracts the subject from the
// request context using the provided function. This is more flexible than
// an interface constraint and works with any context extraction mechanism.
//
// Example with auth.UserID:
//
//	authz.SubjectFromContext(func(ctx context.Context) string {
//	    return auth.UserID(ctx)
//	})
func SubjectFromContext(fn func(context.Context) string) SubjectFunc {
	return func(r *http.Request) string {
		return fn(r.Context())
	}
}

// StaticResource returns a ResourceFunc that always returns a fixed string.
// Use for endpoints where the resource is implicit (e.g., "system", "admin-panel").
func StaticResource(resource string) ResourceFunc {
	return func(_ *http.Request) string {
		return resource
	}
}
