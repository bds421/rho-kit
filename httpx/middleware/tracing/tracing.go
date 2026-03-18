// Package tracing provides OpenTelemetry HTTP middleware for span creation
// and outbound trace context propagation.
package tracing

import (
	"context"
	"net/http"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.30.0"
	"go.opentelemetry.io/otel/trace"

	mw "github.com/bds421/rho-kit/httpx/middleware"
)

const httpTracerName = "kit/http"

// HTTPMiddleware creates an OpenTelemetry span for each HTTP request.
// It extracts incoming W3C trace context from headers and sets standard
// semantic convention attributes. Should be placed early in the middleware
// chain, after RequestID middleware.
func HTTPMiddleware(next http.Handler) http.Handler {
	tracer := otel.Tracer(httpTracerName)
	prop := otel.GetTextMapPropagator()

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Extract incoming trace context from request headers.
		ctx := prop.Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		// Use a conservative initial span name to avoid high-cardinality traces
		// when path parameters are present (e.g. /users/abc123). The span name
		// is updated to r.Pattern after ServeHTTP for Go 1.22+ routers.
		spanName := r.Method

		ctx, span := tracer.Start(ctx, spanName,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				semconv.HTTPRequestMethodKey.String(r.Method),
				semconv.URLPath(r.URL.Path),
				semconv.URLScheme(r.URL.Scheme),
				semconv.ServerAddress(r.Host),
				semconv.UserAgentOriginal(r.UserAgent()),
			),
		)
		defer span.End()

		// Inject trace context into response headers for downstream consumers.
		prop.Inject(ctx, propagation.HeaderCarrier(w.Header()))

		rec := mw.NewResponseRecorder(w)
		next.ServeHTTP(rec, r.WithContext(ctx))

		// Update span name with route pattern if available (more stable than URL path).
		if pattern := r.Pattern; pattern != "" {
			span.SetName(r.Method + " " + pattern)
		}

		span.SetAttributes(
			semconv.HTTPResponseStatusCode(rec.Status()),
		)

		if rec.Status() >= 500 {
			span.SetStatus(codes.Error, http.StatusText(rec.Status()))
		}
	})
}

// InjectHTTPHeaders injects the current trace context into outgoing HTTP
// request headers. Use this when making HTTP calls to other services.
func InjectHTTPHeaders(ctx context.Context, req *http.Request) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
}
