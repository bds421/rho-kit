// Package tracing provides OpenTelemetry HTTP middleware for span creation
// and outbound trace context propagation.
//
// asvs: V7.1.1
package tracing

import (
	"context"
	"net/http"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.30.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/bds421/rho-kit/httpx/v2"
	mw "github.com/bds421/rho-kit/httpx/v2/middleware"
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
				semconv.URLPath(httpx.RequestPath(r)),
				semconv.URLScheme(requestScheme(r)),
				semconv.ServerAddress(r.Host),
				semconv.UserAgentOriginal(r.UserAgent()),
			),
		)
		defer span.End()

		// Inject trace context into response headers for downstream consumers.
		prop.Inject(ctx, propagation.HeaderCarrier(w.Header()))

		// Pass the context-bearing request to next; a ServeMux router sets
		// r.Pattern on the pointer it receives, so finishHTTPSpan must read
		// the same instance (r2) to observe the matched route. Reading the
		// original r would always see an empty Pattern.
		r2 := r.WithContext(ctx)

		rec := mw.NewResponseRecorder(w)
		defer func() {
			recovered := recover()
			finishHTTPSpan(span, r2, rec, recovered != nil)
			if recovered != nil {
				panic(recovered)
			}
		}()
		next.ServeHTTP(rec, r2)
	})
}

func finishHTTPSpan(span trace.Span, r *http.Request, rec *mw.ResponseRecorder, panicked bool) {
	// Update span name with route pattern if available (more stable than URL path).
	// A ServeMux stores the registered pattern in method-qualified form
	// ("GET /users/{id}"), so reuse it verbatim to avoid double-prefixing the
	// method; only synthesize the "{method} {route}" form for bare patterns.
	if pattern := r.Pattern; pattern != "" {
		span.SetName(spanNameForPattern(r.Method, pattern))
	}

	// Hijacked connections (WebSocket, h2 stream takeover) never went
	// through WriteHeader, so rec.Status() is the recorder default
	// (200) — misleading on a connection that may run for hours and
	// has nothing to do with HTTP response semantics. Record 101
	// (Switching Protocols) instead, which matches the actual wire
	// status the upgrade emitted.
	status := rec.Status()
	if rec.WasHijacked() {
		status = http.StatusSwitchingProtocols
	} else if panicked && !rec.WroteHeader() {
		status = http.StatusInternalServerError
	}
	span.SetAttributes(
		semconv.HTTPResponseStatusCode(status),
	)

	if panicked {
		span.SetStatus(codes.Error, "handler panic")
		return
	}
	if status >= 500 {
		span.SetStatus(codes.Error, http.StatusText(status))
	}
}

// spanNameForPattern builds the OTel server span name "{method} {route}" from
// the matched route pattern. ServeMux patterns are method-qualified
// ("GET /users/{id}"); such patterns are used verbatim so the method is not
// duplicated. Bare path patterns ("/users/{id}") are prefixed with the method.
func spanNameForPattern(method, pattern string) string {
	if verb, rest, ok := strings.Cut(pattern, " "); ok && verb == method && rest != "" {
		return pattern
	}
	return method + " " + pattern
}

// requestScheme returns the URL scheme for a server-side request. Go's HTTP
// server leaves r.URL.Scheme empty for origin-form requests (the common case),
// only populating it for absolute-form/proxy URIs, so deriving it from the
// connection's TLS state yields the correct url.scheme semantic attribute.
func requestScheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// InjectHTTPHeaders injects the current trace context into outgoing HTTP
// request headers. Use this when making HTTP calls to other services.
func InjectHTTPHeaders(ctx context.Context, req *http.Request) {
	if req == nil {
		return
	}
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(req.Header))
}
