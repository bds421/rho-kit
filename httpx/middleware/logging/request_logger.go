package logging

import (
	"log/slog"
	"net/http"

	"github.com/bds421/rho-kit/httpx"
)

// WithRequestLogger returns middleware that injects a *slog.Logger into the
// request context with pre-populated attributes (request_id, trace_id, etc.).
// Handler code can retrieve it with httpx.Logger(ctx, fallback).
//
// Attributes are built from the request context at the time the middleware
// runs. Place this middleware after WithRequestID and after tracing
// middleware so the context contains the relevant identifiers.
//
// The extraAttrs functions are called per request to add service-specific
// attributes (e.g., user_id from JWT claims).
func WithRequestLogger(base *slog.Logger, extraAttrs ...func(r *http.Request) slog.Attr) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attrs := make([]slog.Attr, 0, 4)

			if id := httpx.RequestID(r.Context()); id != "" {
				attrs = append(attrs, slog.String("request_id", id))
			}

			if cid := httpx.CorrelationID(r.Context()); cid != "" {
				attrs = append(attrs, slog.String("correlation_id", cid))
			}

			attrs = append(attrs, slog.String("method", r.Method))
			attrs = append(attrs, slog.String("path", r.URL.Path))

			for _, fn := range extraAttrs {
				attrs = append(attrs, fn(r))
			}

			// Convert attrs to slog.With args.
			args := make([]any, len(attrs))
			for i, a := range attrs {
				args[i] = a
			}

			logger := base.With(args...)
			ctx := httpx.SetLogger(r.Context(), logger)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
