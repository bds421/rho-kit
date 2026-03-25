package apperror

import (
	"errors"
	"net/http"
)

// defaultHTTPStatus maps error codes to HTTP status codes.
var defaultHTTPStatus = map[Code]int{
	CodeNotFound:        http.StatusNotFound,
	CodeValidation:      http.StatusBadRequest,
	CodeConflict:        http.StatusConflict,
	CodePermanent:       http.StatusUnprocessableEntity,
	CodeAuthRequired:    http.StatusUnauthorized,
	CodeRateLimit:       http.StatusTooManyRequests,
	CodeOperationFailed: http.StatusInternalServerError,
	CodeForbidden:       http.StatusForbidden,
	CodeUnavailable:     http.StatusBadGateway,
}

// HTTPStatus returns the HTTP status code for the given error.
// Returns http.StatusInternalServerError for non-apperror errors or unknown codes.
//
// For [UnavailableError], the status depends on whether a dependency is identified:
//   - With Dependency set: 502 Bad Gateway (upstream failed)
//   - Without Dependency:  503 Service Unavailable (self is not ready)
func HTTPStatus(err error) int {
	// Special case: UnavailableError without a dependency is 503 (self unavailable),
	// while with a dependency it stays 502 (upstream/bad gateway).
	if ue, ok := AsUnavailable(err); ok && ue.Dependency == "" {
		return http.StatusServiceUnavailable
	}

	var appErr AppError
	if !errors.As(err, &appErr) {
		return http.StatusInternalServerError
	}
	if status, found := defaultHTTPStatus[appErr.ErrorCode()]; found {
		return status
	}
	return http.StatusInternalServerError
}
