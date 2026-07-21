package httpx

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/contextutil"
	"github.com/bds421/rho-kit/httpx/v2/problemdetails"
	"github.com/bds421/rho-kit/observability/v2/logattr"
)

// serviceErrorContext returns logging-safe request attributes regardless of
// whether r is nil. A nil *http.Request collapses to an empty method/path and
// a context.Background() — keeping every error branch panic-free without
// silently dropping logs.
func serviceErrorContext(r *http.Request) (ctx context.Context, method, path string) {
	if r == nil {
		return context.Background(), "", ""
	}
	return r.Context(), r.Method, RequestPath(r)
}

// WriteServiceError maps service-layer error types to appropriate HTTP status codes
// with safe, generic messages that avoid leaking internal details to clients.
// Includes request ID and request details in logs for error correlation.
//
// A nil *http.Request is supported: the error is still written to the
// response, but request-derived log fields (method, path, request ID) are
// empty.
func WriteServiceError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	ctx, method, path := serviceErrorContext(r)
	if r != nil {
		logger = Logger(ctx, logger)
	} else if logger == nil {
		logger = slog.Default()
	}
	switch {
	case apperror.IsValidation(err):
		WriteValidationError(w, logger, err)

	case apperror.IsRateLimit(err):
		if rl, ok := apperror.AsRateLimit(err); ok && rl.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(rl.RetryAfter.Seconds()))))
		}
		WriteError(w, http.StatusTooManyRequests, "rate limit exceeded")

	case apperror.IsNotFound(err):
		WriteError(w, http.StatusNotFound, "resource not found")

	case apperror.IsConflict(err):
		WriteError(w, http.StatusConflict, "resource conflict")

	case apperror.IsPermanent(err):
		WriteError(w, http.StatusUnprocessableEntity, "operation cannot be completed")

	case apperror.IsAuthRequired(err):
		WriteError(w, http.StatusUnauthorized, "authentication required")

	case apperror.IsForbidden(err):
		WriteError(w, http.StatusForbidden, "forbidden")

	case apperror.IsUnavailable(err):
		logAttrs := []any{
			logattr.Error(err),
			logattr.RequestID(contextutil.RequestID(ctx)),
			logattr.Method(method),
			logattr.Path(path),
		}
		if ue, ok := apperror.AsUnavailable(err); ok && ue.Dependency != "" {
			logAttrs = append(logAttrs, slog.String("dependency", ue.Dependency))
		}
		logger.Error("upstream unavailable", logAttrs...)
		// IMPORTANT: Do not send internal error details to clients.
		// The dependency name is only included in logs, not in the response.
		status := HTTPStatus(err)
		msg := "service unavailable"
		if ue, ok := apperror.AsUnavailable(err); ok && ue.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(ue.RetryAfter.Seconds()))))
		}
		WriteError(w, status, msg)

	case apperror.IsStorageFull(err):
		logger.Error("storage capacity exhausted",
			logattr.Error(err),
			logattr.RequestID(contextutil.RequestID(ctx)),
			logattr.Method(method),
			logattr.Path(path),
		)
		// 507 Insufficient Storage: the request was well-formed but
		// the server cannot store the representation needed to
		// complete it. Do not leak the underlying provider error.
		WriteError(w, HTTPStatus(err), "insufficient storage")

	case apperror.IsOperationFailed(err):
		logger.Error("operation failed",
			logattr.Error(err),
			logattr.RequestID(contextutil.RequestID(ctx)),
			logattr.Method(method),
			logattr.Path(path),
		)
		WriteError(w, http.StatusInternalServerError, "internal error")

	default:
		logger.Error("unhandled service error",
			logattr.Error(err),
			logattr.RequestID(contextutil.RequestID(ctx)),
			logattr.Method(method),
			logattr.Path(path),
		)
		WriteError(w, http.StatusInternalServerError, "internal error")
	}
}

// WriteValidationError writes a structured validation error response with
// field-level details when available. logger receives write-failure
// diagnostics (nil falls back to [slog.Default]).
func WriteValidationError(w http.ResponseWriter, logger *slog.Logger, err error) {
	if logger == nil {
		logger = slog.Default()
	}
	ve, ok := apperror.AsValidation(err)
	if !ok || len(ve.Fields) == 0 {
		msg := "validation failed"
		if ok {
			msg = ve.Error()
		}
		WriteError(w, http.StatusBadRequest, msg)
		return
	}

	resp := struct {
		Error  string                `json:"error"`
		Code   string                `json:"code"`
		Fields []apperror.FieldError `json:"fields"`
	}{
		Error:  ve.Error(),
		Code:   string(apperror.CodeValidation),
		Fields: ve.Fields,
	}
	if writeErr := WriteJSON(w, nil, http.StatusBadRequest, resp); writeErr != nil {
		logger.Warn("httpx: write validation error response failed", logattr.Error(writeErr))
	}
}

// WriteServiceProblem is the RFC 7807 sibling of [WriteServiceError].
// It logs the same way (request ID + path + dependency where
// available) and emits an `application/problem+json` response with
// the kit's apperror→Problem mapping plus the request URI as the
// `instance`.
//
// Use this for new services that prefer problem+json — e.g. APIs
// consumed by generated SDKs that expect the RFC 7807 envelope, or
// services that want extension fields (`retry_after_seconds`,
// per-field validation errors) without redefining a JSON shape.
//
// `opts` flow through to [problemdetails.FromError] (e.g.
// `problemdetails.WithBaseURL("https://errors.example.com")` for
// linkable type URIs).
func WriteServiceProblem(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error, opts ...problemdetails.Option) {
	ctx, method, path := serviceErrorContext(r)
	// Resolve the logger the same way WriteServiceError does: the
	// request-scoped logger when r != nil, otherwise the supplied logger or
	// slog.Default. This stops 5xx (unavailable/operation-failed) errors from
	// vanishing silently when the caller passes a nil logger.
	if r != nil {
		logger = Logger(ctx, logger)
	} else if logger == nil {
		logger = slog.Default()
	}
	instance := serviceProblemInstance(r)
	logErr := func(msg string) {
		attrs := []any{
			logattr.Error(err),
			logattr.RequestID(contextutil.RequestID(ctx)),
			logattr.Method(method),
			logattr.Path(path),
		}
		if ue, ok := apperror.AsUnavailable(err); ok && ue.Dependency != "" {
			attrs = append(attrs, slog.String("dependency", ue.Dependency))
		}
		logger.Error(msg, attrs...)
	}

	switch {
	case apperror.IsValidation(err), apperror.IsRateLimit(err),
		apperror.IsNotFound(err), apperror.IsConflict(err),
		apperror.IsPermanent(err), apperror.IsAuthRequired(err),
		apperror.IsForbidden(err):
		// Client-recoverable: caller may add their own audit-level event.
	case apperror.IsUnavailable(err):
		logErr("upstream unavailable")
	case apperror.IsOperationFailed(err):
		logErr("operation failed")
	default:
		logErr("unhandled service error")
	}

	// Mirror WriteServiceError: emit the RFC-compliant Retry-After header so
	// standard clients, proxies, and CDNs honoring the header (not just the
	// retry_after_seconds body extension) get correct backoff signaling.
	if rl, ok := apperror.AsRateLimit(err); ok && rl.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(rl.RetryAfter.Seconds()))))
	} else if ue, ok := apperror.AsUnavailable(err); ok && ue.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(ue.RetryAfter.Seconds()))))
	}

	allOpts := append([]problemdetails.Option{
		problemdetails.WithInstance(instance),
	}, opts...)
	problemdetails.Write(w, problemdetails.FromError(err, allOpts...))
}

func serviceProblemInstance(r *http.Request) string {
	return RequestPath(r)
}
