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
func RequirePermission(policy Policy, action string, resource ResourceFunc, subject SubjectFunc, opts ...MiddlewareOption) func(http.Handler) http.Handler {
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

// SubjectFromHeader returns a SubjectFunc that reads a header value.
func SubjectFromHeader(header string) SubjectFunc {
	return func(r *http.Request) string {
		return r.Header.Get(header)
	}
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
