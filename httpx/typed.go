package httpx

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/bds421/rho-kit/core/validate"
)

// JSON returns an http.Handler that decodes a JSON request body,
// validates it, calls fn, and encodes the response as JSON with status 200.
//
// Note: validation errors from [validate.Struct] are passed through
// [WriteServiceError], which may expose struct field names and validation
// rules in the error response. This is acceptable for internal APIs. For
// public-facing APIs, use [WithErrorHandler] to customize error responses
// or validate manually before calling the typed handler.
func JSON[Req, Resp any](logger *slog.Logger, fn func(ctx context.Context, r *http.Request, req Req) (Resp, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Req
		if !DecodeJSON(w, r, &req) {
			return
		}
		if err := validate.Struct(req); err != nil {
			WriteServiceError(w, r, logger, err)
			return
		}

		resp, err := fn(r.Context(), r, req)
		if err != nil {
			WriteServiceError(w, r, logger, err)
			return
		}
		WriteJSON(w, http.StatusOK, resp)
	})
}

// JSONNoBody returns an http.Handler with no request body decoding.
// The handler receives the full *http.Request for path parameters, query strings, etc.
func JSONNoBody[Resp any](logger *slog.Logger, fn func(ctx context.Context, r *http.Request) (Resp, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := fn(r.Context(), r)
		if err != nil {
			WriteServiceError(w, r, logger, err)
			return
		}
		WriteJSON(w, http.StatusOK, resp)
	})
}

// JSONStatus returns an http.Handler that decodes a JSON request body,
// validates it, and lets the handler specify the HTTP status code.
func JSONStatus[Req, Resp any](logger *slog.Logger, fn func(ctx context.Context, r *http.Request, req Req) (int, Resp, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Req
		if !DecodeJSON(w, r, &req) {
			return
		}
		if err := validate.Struct(req); err != nil {
			WriteServiceError(w, r, logger, err)
			return
		}

		status, resp, err := fn(r.Context(), r, req)
		if err != nil {
			WriteServiceError(w, r, logger, err)
			return
		}
		WriteJSON(w, status, resp)
	})
}

// JSONNoBodyStatus returns an http.Handler with no request body decoding that
// lets the handler specify the HTTP status code. Useful for endpoints that need
// to return different success statuses (e.g. 200, 202, 204) without a request body.
func JSONNoBodyStatus[Resp any](logger *slog.Logger, fn func(ctx context.Context, r *http.Request) (int, Resp, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, resp, err := fn(r.Context(), r)
		if err != nil {
			WriteServiceError(w, r, logger, err)
			return
		}
		WriteJSON(w, status, resp)
	})
}

// NoContent returns an http.Handler that calls fn and returns 204 No Content
// on success. Use for DELETE endpoints or actions with no response body.
func NoContent(logger *slog.Logger, fn func(ctx context.Context, r *http.Request) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := fn(r.Context(), r); err != nil {
			WriteServiceError(w, r, logger, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// --- Mux-bound convenience wrappers ---

// Handle registers a typed handler on the mux. See [JSON] for the decoupled version.
func Handle[Req, Resp any](mux *http.ServeMux, pattern string, logger *slog.Logger, fn func(ctx context.Context, r *http.Request, req Req) (Resp, error)) {
	mux.Handle(pattern, JSON[Req, Resp](logger, fn))
}

// HandleNoBody registers a typed handler with no request body on the mux.
// See [JSONNoBody] for the decoupled version.
func HandleNoBody[Resp any](mux *http.ServeMux, pattern string, logger *slog.Logger, fn func(ctx context.Context, r *http.Request) (Resp, error)) {
	mux.Handle(pattern, JSONNoBody[Resp](logger, fn))
}

// HandleStatus registers a typed handler with custom status code on the mux.
// See [JSONStatus] for the decoupled version.
func HandleStatus[Req, Resp any](mux *http.ServeMux, pattern string, logger *slog.Logger, fn func(ctx context.Context, r *http.Request, req Req) (int, Resp, error)) {
	mux.Handle(pattern, JSONStatus[Req, Resp](logger, fn))
}

// HandleNoBodyStatus registers a typed handler with no request body and custom
// status code on the mux. See [JSONNoBodyStatus] for the decoupled version.
func HandleNoBodyStatus[Resp any](mux *http.ServeMux, pattern string, logger *slog.Logger, fn func(ctx context.Context, r *http.Request) (int, Resp, error)) {
	mux.Handle(pattern, JSONNoBodyStatus[Resp](logger, fn))
}
