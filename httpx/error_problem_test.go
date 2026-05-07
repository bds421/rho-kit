package httpx

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/apperror"
	"github.com/bds421/rho-kit/httpx/problemdetails"
)

func TestWriteServiceProblem_NotFound(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/widgets/42?ref=home", nil)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	WriteServiceProblem(rec, req, logger, apperror.NewNotFound("widget", "42"))

	resp := rec.Result()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Equal(t, problemdetails.ContentType, resp.Header.Get("Content-Type"))

	var p map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&p))
	assert.Equal(t, float64(http.StatusNotFound), p["status"])
	// instance must include the request URI so consumers can correlate
	// the failure to the originating request without a separate header.
	require.NotNil(t, p["instance"])
	assert.True(t, strings.HasPrefix(p["instance"].(string), "/widgets/42"),
		"instance should reflect the request path, got %q", p["instance"])
}

func TestWriteServiceProblem_ValidationFieldsAsExtensions(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/items", nil)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	verr := apperror.NewFieldValidation(apperror.FieldError{
		Field:   "name",
		Message: "required",
	})
	WriteServiceProblem(rec, req, logger, verr)

	resp := rec.Result()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, problemdetails.ContentType, resp.Header.Get("Content-Type"))

	var p map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&p))
	// Validation field errors are surfaced as the `errors` extension —
	// not as a redefined envelope.
	assert.NotNil(t, p["errors"], "validation Problem should expose `errors` extension")
}

func TestWriteServiceProblem_BaseURLOpt(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	WriteServiceProblem(rec, req, logger,
		apperror.NewNotFound("x", "1"),
		problemdetails.WithBaseURL("https://errors.example.com"))

	var p map[string]any
	require.NoError(t, json.NewDecoder(rec.Result().Body).Decode(&p))
	typ, _ := p["type"].(string)
	assert.True(t, strings.HasPrefix(typ, "https://errors.example.com/"),
		"type should be a documentation URI, got %q", typ)
}
