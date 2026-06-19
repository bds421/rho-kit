// Package openapi adapts oapi-codegen's StrictServer pattern to the
// kit's middleware/error model. v2 added this so contract-first APIs
// — the OpenAPI workflow — get the kit's auth + tenant + budget +
// problem-details + tracing decorators automatically, without
// per-handler boilerplate.
//
// Usage:
//
//  1. Generate a strict server with oapi-codegen
//     (`oapi-codegen --generate=strict-server,types -package api spec.yaml`).
//
//  2. Implement the generated StrictServerInterface.
//
//  3. Translate strict-server errors into Problem Details responses by
//     passing [StrictErrorHandler] to the generated constructor, then
//     wire the handler through openapi.Mount with the kit's middleware:
//
//     opts := api.StrictHTTPServerOptions{
//     ResponseErrorHandlerFunc: openapi.StrictErrorHandler(nil),
//     }
//     handler := api.NewStrictHandlerWithOptions(impl, nil, opts)
//     mux := http.NewServeMux()
//     mux.Handle("/api/v1/", openapi.Mount(handler, openapi.Options{
//     Logger: slog.Default(),
//     }))
//
// The Mount call adds correlation ID propagation, request logging, and
// panic recovery in the recommended order. Error translation
// (apperror → RFC 7807 Problem Details) happens at the strict-handler
// boundary via [StrictErrorHandler]; oapi-codegen converts a returned
// error into a response before it reaches an outer http.Handler, so
// Mount itself cannot intercept it.
//
// The adapter does NOT install auth or rate-limiting — those are
// app.Builder's responsibility because they apply uniformly across
// every mounted handler. openapi.Mount focuses on the OpenAPI-shaped
// concerns (request validation, problem details, structured errors).
//
// asvs: V13.1.1, V5.3.1
package openapi

import (
	"log/slog"
	"net/http"

	"github.com/bds421/rho-kit/httpx/v2/middleware/correlationid"
	"github.com/bds421/rho-kit/httpx/v2/middleware/logging"
	"github.com/bds421/rho-kit/httpx/v2/middleware/recover"
	"github.com/bds421/rho-kit/httpx/v2/middleware/requestid"
	"github.com/bds421/rho-kit/httpx/v2/problemdetails"
)

// Options configure [Mount].
type Options struct {
	// Logger is the slog logger used by the access-log middleware.
	// Defaults to slog.Default() when nil.
	Logger *slog.Logger

	// QuietPaths is the set of routes logged at debug level instead
	// of info. Typically the readiness probes the OpenAPI spec
	// describes.
	QuietPaths []string

	// ErrorMapper translates an error returned by the strict server
	// implementation into a [problemdetails.Problem]. The kit ships
	// a default [DefaultErrorMapper] that handles apperror types;
	// override only when the service has custom error categories.
	//
	// Mount does NOT consume this field: oapi-codegen converts a
	// strict-server error into a response inside the generated handler,
	// before it reaches Mount's outer middleware chain. Pass the mapper
	// to [StrictErrorHandler] and wire the result through the generated
	// constructor's ResponseErrorHandlerFunc instead. ErrorMapper is
	// retained only so existing call sites keep compiling.
	//
	// Deprecated: Mount ignores ErrorMapper. Use [StrictErrorHandler]
	// at the strict-handler boundary.
	ErrorMapper func(error) problemdetails.Problem
}

// Mount wraps handler with the kit's standard OpenAPI-handler
// decoration: panic recovery, request ID, correlation ID, and a
// structured access log.
//
// The order matters: recovery is outermost so a panic anywhere
// downstream becomes a 500 response; request ID is next, then
// correlation ID, then logging innermost so access-log lines carry both
// IDs.
//
// Because recovery is outermost, it runs against the original request
// whose context predates the request-ID and correlation-ID middleware
// (both inject their IDs via r.WithContext into the downstream copy).
// Recovery's panic log line and the 500 JSON body therefore do NOT carry
// a request ID or correlation ID — only the inner access log does. The
// X-Request-Id / X-Correlation-Id response headers are still set, since
// those middleware write them onto the shared ResponseWriter.
//
// Mount does not translate strict-server errors into Problem Details:
// oapi-codegen converts a returned error into a response inside the
// generated handler, before it reaches this middleware chain. Wire
// [StrictErrorHandler] through the generated constructor's
// ResponseErrorHandlerFunc to get apperror → RFC 7807 translation.
//
// Returns a [http.Handler] ready to mount under any route prefix.
// Most consumers `mux.Handle("/api/v1/", openapi.Mount(...))`.
func Mount(handler http.Handler, opts Options) http.Handler {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	wrapped := handler
	wrapped = logging.Logger(opts.Logger, opts.QuietPaths)(wrapped)
	wrapped = correlationid.WithCorrelationID(wrapped)
	wrapped = requestid.WithRequestID(wrapped)
	wrapped = recover.Middleware(recover.WithLogger(opts.Logger))(wrapped)
	return wrapped
}

// DefaultErrorMapper translates kit error types into RFC 7807
// Problem Details bodies. Unknown errors map to 500 Internal Server
// Error with a generic title to avoid leaking internals.
//
// Strict-server consumers return errors directly from their
// implementation; this mapper is what turns those errors into HTTP
// responses without per-handler if/else chains.
func DefaultErrorMapper(err error) problemdetails.Problem {
	if err == nil {
		return problemdetails.Problem{Status: http.StatusOK}
	}
	return problemdetails.FromError(err)
}

// StrictErrorHandler returns a response error handler that translates an
// error returned by a strict-server implementation into an RFC 7807
// Problem Details response.
//
// It is the wiring point for the [Options.ErrorMapper] policy: pass the
// result as the ResponseErrorHandlerFunc on oapi-codegen's
// StrictHTTPServerOptions, e.g.
//
//	opts := api.StrictHTTPServerOptions{
//	    ResponseErrorHandlerFunc: openapi.StrictErrorHandler(nil),
//	}
//	handler := api.NewStrictHandlerWithOptions(impl, nil, opts)
//
// The returned function's signature matches oapi-codegen's
// ResponseErrorHandlerFunc, so it is assignable without importing the
// generated package here.
//
// When mapper is nil, [DefaultErrorMapper] is used. The mapped Problem is
// written via [problemdetails.Write], which sets the problem+json media
// type and a 500 status when the mapper leaves [problemdetails.Problem.Status]
// unset.
func StrictErrorHandler(mapper func(error) problemdetails.Problem) func(http.ResponseWriter, *http.Request, error) {
	if mapper == nil {
		mapper = DefaultErrorMapper
	}
	return func(w http.ResponseWriter, _ *http.Request, err error) {
		problemdetails.Write(w, mapper(err))
	}
}
