package openapigen

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	jsonschema "github.com/google/jsonschema-go/jsonschema"

	"github.com/bds421/rho-kit/httpx/v2"
)

// errNilMux is returned when the caller passes a nil mux to one of
// the Handle helpers.
var errNilMux = errors.New("openapigen: mux must not be nil")

// errNilSpec is returned when the caller passes a nil spec to one of
// the Handle helpers.
var errNilSpec = errors.New("openapigen: spec must not be nil")

// Handle registers a typed handler on both the mux AND the spec
// atomically. The signature mirrors [httpx.Handle] with a leading
// *Spec parameter and the pattern split into (method, path) so the
// spec sees an OpenAPI-canonical path string.
//
// The handler accepts a request body of type Req and returns a Resp
// at HTTP 200. Use [HandleStatus] for handlers that need to choose a
// success status at runtime.
//
// Pattern format: Go's [net/http.ServeMux] accepts patterns shaped as
// "METHOD /path" (e.g. "POST /widgets") and exposes path wildcards
// like "/widgets/{id}". The spec records `path` as written; callers
// that use wildcards should pre-format them as OpenAPI path templates
// (which happen to use the same `{name}` syntax).
//
// Behaviour notes:
//
//   - The mux registration is performed via mux.Handle on the
//     NORMALISED "METHOD /path" pattern (matching the verb recorded in
//     the spec) so the verb participates in the stdlib's routing match.
//     The stdlib matches methods case-sensitively, so a caller passing
//     a lowercase/mixed-case verb (accepted by [Spec.Register]) still
//     yields a route real requests can reach.
//   - The OpenAPI registration uses the same path; the verb is recorded
//     on the operation rather than the path string.
//   - If the spec registration fails (e.g. schema generation), the
//     route is NOT mounted on the mux and the error is returned. This
//     trades convenience for fail-fast: a typed handler whose schema
//     refuses to build is a programming bug surfaced at boot.
func Handle[Req, Resp any](
	mux *http.ServeMux,
	spec *Spec,
	method, path string,
	logger *slog.Logger,
	fn func(ctx context.Context, r *http.Request, req Req) (Resp, error),
	opts ...RouteOption,
) error {
	if mux == nil {
		return errNilMux
	}
	if spec == nil {
		return errNilSpec
	}
	allOpts := []RouteOption{WithRequestType[Req]()}
	if !hasResponseOption(opts) {
		allOpts = append(allOpts, WithResponseType[Resp](http.StatusOK))
	}
	allOpts = append(allOpts, opts...)
	if err := spec.Register(method, path, allOpts...); err != nil {
		return err
	}
	mux.Handle(muxPattern(method, path), httpx.JSON[Req, Resp](logger, fn))
	return nil
}

// HandleStatus registers a typed handler whose response status is
// chosen at runtime. The OpenAPI operation records the status passed
// via [WithResponseType] (or, if none was supplied, defaults to 200
// for the Resp schema).
//
// Returns an error on spec-registration failure (the route is not
// mounted on the mux in that case).
func HandleStatus[Req, Resp any](
	mux *http.ServeMux,
	spec *Spec,
	method, path string,
	logger *slog.Logger,
	fn func(ctx context.Context, r *http.Request, req Req) (int, Resp, error),
	opts ...RouteOption,
) error {
	if mux == nil {
		return errNilMux
	}
	if spec == nil {
		return errNilSpec
	}
	allOpts := []RouteOption{WithRequestType[Req]()}
	if !hasResponseOption(opts) {
		allOpts = append(allOpts, WithResponseType[Resp](http.StatusOK))
	}
	allOpts = append(allOpts, opts...)
	if err := spec.Register(method, path, allOpts...); err != nil {
		return err
	}
	mux.Handle(muxPattern(method, path), httpx.JSONStatus[Req, Resp](logger, fn))
	return nil
}

// HandleNoBody registers a typed handler with no request body. The
// spec records no requestBody for the operation.
func HandleNoBody[Resp any](
	mux *http.ServeMux,
	spec *Spec,
	method, path string,
	logger *slog.Logger,
	fn func(ctx context.Context, r *http.Request) (Resp, error),
	opts ...RouteOption,
) error {
	if mux == nil {
		return errNilMux
	}
	if spec == nil {
		return errNilSpec
	}
	allOpts := []RouteOption(nil)
	if !hasResponseOption(opts) {
		allOpts = append(allOpts, WithResponseType[Resp](http.StatusOK))
	}
	allOpts = append(allOpts, opts...)
	if err := spec.Register(method, path, allOpts...); err != nil {
		return err
	}
	mux.Handle(muxPattern(method, path), httpx.JSONNoBody[Resp](logger, fn))
	return nil
}

// HandleNoBodyStatus registers a typed handler with no request body
// and a runtime-chosen status code.
func HandleNoBodyStatus[Resp any](
	mux *http.ServeMux,
	spec *Spec,
	method, path string,
	logger *slog.Logger,
	fn func(ctx context.Context, r *http.Request) (int, Resp, error),
	opts ...RouteOption,
) error {
	if mux == nil {
		return errNilMux
	}
	if spec == nil {
		return errNilSpec
	}
	allOpts := []RouteOption(nil)
	if !hasResponseOption(opts) {
		allOpts = append(allOpts, WithResponseType[Resp](http.StatusOK))
	}
	allOpts = append(allOpts, opts...)
	if err := spec.Register(method, path, allOpts...); err != nil {
		return err
	}
	mux.Handle(muxPattern(method, path), httpx.JSONNoBodyStatus[Resp](logger, fn))
	return nil
}

// HandleNoContent registers a 204 handler on both mux and spec.
func HandleNoContent(
	mux *http.ServeMux,
	spec *Spec,
	method, path string,
	logger *slog.Logger,
	fn func(ctx context.Context, r *http.Request) error,
	opts ...RouteOption,
) error {
	if mux == nil {
		return errNilMux
	}
	if spec == nil {
		return errNilSpec
	}
	allOpts := []RouteOption{WithResponseStatus(http.StatusNoContent, http.StatusText(http.StatusNoContent))}
	allOpts = append(allOpts, opts...)
	if err := spec.Register(method, path, allOpts...); err != nil {
		return err
	}
	mux.Handle(muxPattern(method, path), httpx.NoContent(logger, fn))
	return nil
}

// muxPattern builds the [net/http.ServeMux] pattern for a route using
// the same verb normalisation that [Spec.Register] applies, so the mux
// route always matches the operation the spec advertises. The method
// has already been validated by Register by the time this runs; the raw
// token is only used as a defensive fallback for an unrecognised verb
// (which cannot happen on the success path).
func muxPattern(method, path string) string {
	if norm, ok := normaliseMethod(method); ok {
		return norm + " " + path
	}
	return method + " " + path
}

// hasResponseOption inspects opts via a probe routeConfig to detect
// whether the caller already supplied a response body schema or
// status. This avoids the default 200 schema clobbering a
// caller-supplied alternative (e.g. 201 Created for a POST handler).
//
// The probe is a no-op evaluation: each option is applied to a
// disposable routeConfig and we read the result. Option errors are
// ignored here — they surface for real during Spec.Register below.
func hasResponseOption(opts []RouteOption) bool {
	probe := routeConfig{
		responseDescriptions: map[int]string{},
		responseSchemas:      map[int]*jsonschema.Schema{},
		responseTypes:        map[int]string{},
	}
	for _, o := range opts {
		if o == nil {
			continue
		}
		_ = o(&probe)
	}
	// Descriptions alone (WithResponseDescription) and headers do NOT
	// suppress the default success schema — only an explicit body schema,
	// multi-content registration, or body-less WithResponseStatus does.
	return len(probe.responseSchemas) > 0 ||
		len(probe.responseExtraContent) > 0 ||
		len(probe.responseBodyless) > 0
}
