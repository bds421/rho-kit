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
}

// HTTPStatus returns the HTTP status code for the given error.
// Returns http.StatusInternalServerError for non-apperror errors or unknown codes.
func HTTPStatus(err error) int {
	var appErr AppError
	if !errors.As(err, &appErr) {
		return http.StatusInternalServerError
	}
	if status, found := defaultHTTPStatus[appErr.ErrorCode()]; found {
		return status
	}
	return http.StatusInternalServerError
}
