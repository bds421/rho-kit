package apperror

import (
	"errors"
	"net/http"
)

// defaultHTTPStatus maps error codes to HTTP status codes.
//
// Deprecated: This map is an internal detail of the deprecated [HTTPStatus]
// function. Use httpx.HTTPStatus instead.
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
//
// Deprecated: HTTP status mapping is a transport concern. Use [httpx.HTTPStatus]
// instead. This function will be removed in the next major version.
func HTTPStatus(err error) int {
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
