package httpx

import (
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/bds421/rho-kit/core/apperror"
	"github.com/bds421/rho-kit/observability/logattr"
)

// WriteServiceError maps service-layer error types to appropriate HTTP status codes
// with safe, generic messages that avoid leaking internal details to clients.
// Includes request ID and request details in logs for error correlation.
func WriteServiceError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
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
			logattr.RequestID(RequestID(r.Context())),
			logattr.Method(r.Method),
			logattr.Path(r.URL.Path),
		}
		if ue, ok := apperror.AsUnavailable(err); ok && ue.Dependency != "" {
			logAttrs = append(logAttrs, slog.String("dependency", ue.Dependency))
		}
		logger.Error("upstream unavailable", logAttrs...)
		// IMPORTANT: Do not send internal error details to clients.
		// The dependency name is only included in logs, not in the response.
		status := apperror.HTTPStatus(err)
		msg := "service unavailable"
		// Set Retry-After header: use the error's RetryAfter if present,
		// otherwise default to 5s for 503 responses.
		if ue, ok := apperror.AsUnavailable(err); ok && ue.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(ue.RetryAfter.Seconds()))))
		} else if status == http.StatusServiceUnavailable {
			w.Header().Set("Retry-After", "5")
		}
		WriteError(w, status, msg)

	case apperror.IsOperationFailed(err):
		logger.Error("operation failed",
			logattr.Error(err),
			logattr.RequestID(RequestID(r.Context())),
			logattr.Method(r.Method),
			logattr.Path(r.URL.Path),
		)
		// OperationFailedError.Error() is sent to the client as-is.
		// IMPORTANT: Callers must ensure the message is client-safe and does
		// not contain internal details (hostnames, ports, stack traces).
		// When in doubt, use a generic message like "operation failed".
		msg := "internal error"
		if opErr, ok := apperror.AsOperationFailed(err); ok && opErr.Error() != "" {
			msg = opErr.Error()
		}
		WriteError(w, http.StatusInternalServerError, msg)

	default:
		logger.Error("unhandled service error",
			logattr.Error(err),
			logattr.RequestID(RequestID(r.Context())),
			logattr.Method(r.Method),
			logattr.Path(r.URL.Path),
		)
		WriteError(w, http.StatusInternalServerError, "internal error")
	}
}

// WriteValidationError writes a structured validation error response with
// field-level details when available.
func WriteValidationError(w http.ResponseWriter, logger *slog.Logger, err error) {
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
		Code:   "VALIDATION",
		Fields: ve.Fields,
	}
	WriteJSON(w, http.StatusBadRequest, resp)
}
