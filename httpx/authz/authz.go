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
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"

	kitauthz "github.com/bds421/rho-kit/authz/v2"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/httpx/v2"
	"github.com/bds421/rho-kit/httpx/v2/internal/headerutil"
	"golang.org/x/net/http/httpguts"
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
	if action == "" {
		panic("authz: RequirePermission requires a non-empty action")
	}
	if err := kitauthz.ValidateRequest(kitauthz.Request{Subject: "subject", Action: action, Resource: "resource"}); err != nil {
		panic("authz: RequirePermission requires a valid action")
	}
	cfg := middlewareConfig{
		logger: slog.Default(),
	}
	for _, o := range opts {
		if o == nil {
			panic("authz: RequirePermission middleware option must not be nil")
		}
		o(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sub, ok := safeSubject(cfg.logger, subject, r)
			if !ok {
				httpx.WriteError(w, http.StatusUnauthorized, "missing subject identity")
				return
			}
			if sub == "" {
				httpx.WriteError(w, http.StatusUnauthorized, "missing subject identity")
				return
			}
			if err := kitauthz.ValidateRequest(kitauthz.Request{Subject: sub, Action: action, Resource: "resource"}); err != nil {
				cfg.logger.Warn("authorization invalid subject",
					redact.Error(err),
					"action", action,
				)
				httpx.WriteError(w, http.StatusUnauthorized, "missing subject identity")
				return
			}
			res, ok := safeResource(cfg.logger, resource, r)
			if !ok {
				httpx.WriteError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if err := kitauthz.ValidateRequest(kitauthz.Request{Subject: sub, Action: action, Resource: res}); err != nil {
				cfg.logger.Warn("authorization invalid request",
					redact.Error(err),
					"action", action,
				)
				httpx.WriteError(w, http.StatusForbidden, "forbidden")
				return
			}

			allowed, err := safeAllowed(cfg.logger, policy, r.Context(), sub, action, res)
			if err != nil {
				cfg.logger.Error("authorization policy error",
					redact.Error(err),
					redact.String("subject", sub),
					"action", action,
					redact.String("resource", res),
				)
				httpx.WriteError(w, http.StatusInternalServerError, "internal error")
				return
			}
			if !allowed {
				cfg.logger.Warn("authorization denied",
					redact.String("subject", sub),
					"action", action,
					redact.String("resource", res),
				)
				httpx.WriteError(w, http.StatusForbidden, "forbidden")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func safeSubject(logger *slog.Logger, subject SubjectFunc, r *http.Request) (value string, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			logger.Error("authorization subject extractor panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			value, ok = "", false
		}
	}()
	return subject(r), true
}

func safeResource(logger *slog.Logger, resource ResourceFunc, r *http.Request) (value string, ok bool) {
	defer func() {
		if rec := recover(); rec != nil {
			logger.Error("authorization resource extractor panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			value, ok = "", false
		}
	}()
	return resource(r), true
}

func safeAllowed(logger *slog.Logger, policy Policy, ctx context.Context, subject, action, resource string) (allowed bool, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			logger.Error("authorization policy panicked",
				redact.Panic(rec),
				redact.String("subject", subject),
				"action", action,
				redact.String("resource", resource),
				"stack", string(debug.Stack()),
			)
			allowed, err = false, fmt.Errorf("authorization policy panicked")
		}
	}()
	return policy.Allowed(ctx, subject, action, resource)
}

// MiddlewareOption configures the authorization middleware.
type MiddlewareOption func(*middlewareConfig)

type middlewareConfig struct {
	logger *slog.Logger
}

// WithLogger sets the logger for authorization errors. A nil logger is
// normalized to [slog.Default] so test wiring stays ergonomic; the
// middleware never holds a nil slog.Logger.
func WithLogger(l *slog.Logger) MiddlewareOption {
	return func(c *middlewareConfig) {
		if l == nil {
			c.logger = slog.Default()
			return
		}
		c.logger = l
	}
}

// --- Extractor helpers ---

// ResourceFromPath returns a ResourceFunc that extracts a path parameter by name.
func ResourceFromPath(param string) ResourceFunc {
	if param == "" {
		panic("authz: ResourceFromPath requires a non-empty parameter name")
	}
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
	if !httpguts.ValidHeaderFieldName(header) {
		panic("authz: SubjectFromUntrustedHeader requires a valid non-empty header")
	}
	return func(r *http.Request) string {
		return singletonIdentityHeader(r.Header, header)
	}
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
	if !httpguts.ValidHeaderFieldName(header) {
		panic("authz: SubjectFromTrustedHeader requires a valid non-empty header")
	}
	trustedProxies = cloneIPNets(trustedProxies)
	return func(r *http.Request) string {
		if !remoteAddrInTrustedProxies(r.RemoteAddr, trustedProxies) {
			return ""
		}
		return singletonIdentityHeader(r.Header, header)
	}
}

func singletonIdentityHeader(h http.Header, name string) string {
	value, ok := headerutil.SingletonIdentity(h, name)
	if !ok {
		return ""
	}
	return value
}

func cloneIPNets(in []*net.IPNet) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(in))
	for _, n := range in {
		if n == nil {
			out = append(out, nil)
			continue
		}
		out = append(out, &net.IPNet{
			IP:   append(net.IP(nil), n.IP...),
			Mask: append(net.IPMask(nil), n.Mask...),
		})
	}
	return out
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
	if fn == nil {
		panic("authz: SubjectFromContext requires a non-nil function")
	}
	return func(r *http.Request) string {
		return fn(r.Context())
	}
}

// StaticResource returns a ResourceFunc that always returns a fixed string.
// Use for endpoints where the resource is implicit (e.g., "system", "admin-panel").
func StaticResource(resource string) ResourceFunc {
	if resource == "" {
		panic("authz: StaticResource requires a non-empty resource")
	}
	if err := kitauthz.ValidateRequest(kitauthz.Request{Subject: "subject", Action: "action", Resource: resource}); err != nil {
		panic("authz: StaticResource requires a valid resource")
	}
	return func(_ *http.Request) string {
		return resource
	}
}
