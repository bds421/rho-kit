package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bds421/rho-kit/core/apperror"
)

func TestWriteServiceError_NilLogger_UnhandledError(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	// Must not panic when logger is nil; falls back to slog.Default().
	WriteServiceError(rec, req, nil, errors.New("boom"))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestWriteServiceError_NilLogger_UnavailableError(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	WriteServiceError(rec, req, nil, apperror.NewUnavailable("db unavailable"))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestWriteServiceError_NilLogger_OperationFailedError(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)

	WriteServiceError(rec, req, nil, apperror.NewOperationFailed("operation failed"))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}
