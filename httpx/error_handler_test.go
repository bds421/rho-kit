package httpx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bds421/rho-kit/core/v2/apperror"
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

func TestWriteServiceError_NilRequest_UnhandledError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteServiceError(rec, nil, nil, errors.New("boom"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestWriteServiceError_NilRequest_UnavailableError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteServiceError(rec, nil, nil, apperror.NewUnavailable("db unavailable"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func TestWriteServiceError_NilRequest_OperationFailedError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteServiceError(rec, nil, nil, apperror.NewOperationFailed("operation failed"))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestWriteServiceError_NilRequest_ValidationError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteServiceError(rec, nil, nil, apperror.NewValidation("bad input"))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

func TestWriteServiceError_NilRequest_NotFoundError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteServiceError(rec, nil, nil, apperror.NewNotFound("entity", "1"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestWriteServiceProblem_NilRequest_UnhandledError(t *testing.T) {
	rec := httptest.NewRecorder()
	// Must not panic; logger is nil so no log path is exercised, but writer
	// should still produce the problem+json response.
	WriteServiceProblem(rec, nil, nil, errors.New("boom"))
	if rec.Code == 0 {
		t.Fatal("expected response to be written")
	}
}

func TestWriteServiceProblem_NilRequest_UnavailableError(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteServiceProblem(rec, nil, nil, apperror.NewUnavailable("db unavailable"))
	if rec.Code == 0 {
		t.Fatal("expected response to be written")
	}
}
