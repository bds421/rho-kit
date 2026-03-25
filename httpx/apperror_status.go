package httpx

import (
	"errors"
	"net/http"

	"github.com/bds421/rho-kit/core/apperror"
)

// defaultHTTPStatus maps error codes to HTTP status codes.
var defaultHTTPStatus = map[apperror.Code]int{
	apperror.CodeNotFound:        http.StatusNotFound,
	apperror.CodeValidation:      http.StatusBadRequest,
	apperror.CodeConflict:        http.StatusConflict,
	apperror.CodePermanent:       http.StatusUnprocessableEntity,
	apperror.CodeAuthRequired:    http.StatusUnauthorized,
	apperror.CodeRateLimit:       http.StatusTooManyRequests,
	apperror.CodeOperationFailed: http.StatusInternalServerError,
	apperror.CodeForbidden:       http.StatusForbidden,
	apperror.CodeUnavailable:     http.StatusBadGateway,
}

// HTTPStatus returns the HTTP status code for the given error.
// Returns http.StatusInternalServerError for non-apperror errors or unknown codes.
//
// For [apperror.UnavailableError], the status depends on whether a dependency is identified:
//   - With Dependency set: 502 Bad Gateway (upstream failed)
//   - Without Dependency:  503 Service Unavailable (self is not ready)
func HTTPStatus(err error) int {
	// Special case: UnavailableError without a dependency is 503 (self unavailable),
	// while with a dependency it stays 502 (upstream/bad gateway).
	if ue, ok := apperror.AsUnavailable(err); ok && ue.Dependency == "" {
		return http.StatusServiceUnavailable
	}

	var appErr apperror.AppError
	if !errors.As(err, &appErr) {
		return http.StatusInternalServerError
	}
	if status, found := defaultHTTPStatus[appErr.ErrorCode()]; found {
		return status
	}
	return http.StatusInternalServerError
}
