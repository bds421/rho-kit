package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/httpx/v2/problemdetails"
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
	assert.Equal(t, "/widgets/42", p["instance"],
		"instance should reflect the request path without query parameters")
	assert.NotContains(t, p["instance"], "ref=home")
}

func TestWriteServiceProblem_InstanceDoesNotLeakQuerySecrets(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=secret-code&state=secret-state", nil)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	WriteServiceProblem(rec, req, logger, errors.New("handler failed"))

	var p map[string]any
	require.NoError(t, json.NewDecoder(rec.Result().Body).Decode(&p))
	assert.Equal(t, "/oauth/callback", p["instance"])
	assert.NotContains(t, p["instance"], "secret-code")
	assert.NotContains(t, p["instance"], "secret-state")
}

func TestWriteServiceProblem_InstanceUsesEscapedPath(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/files/a%2Fb", nil)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	WriteServiceProblem(rec, req, logger, errors.New("handler failed"))

	var p map[string]any
	require.NoError(t, json.NewDecoder(rec.Result().Body).Decode(&p))
	assert.Equal(t, "/files/a%2Fb", p["instance"])
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

func TestWriteServiceProblem_DoesNotLeakUnavailableDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	WriteServiceProblem(rec, req, logger,
		apperror.NewDependencyUnavailable(
			"postgres",
			"dial tcp 10.0.0.5:5432 for postgres://user:secret@db/app",
			nil,
		))

	var p map[string]any
	require.NoError(t, json.NewDecoder(rec.Result().Body).Decode(&p))
	assert.Equal(t, "service unavailable", p["detail"])
	assert.NotContains(t, p["detail"], "10.0.0.5")
	assert.NotContains(t, p["detail"], "secret")
}

func TestWriteServiceProblem_SetsRetryAfterHeaderForUnavailable(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	err := apperror.NewUnavailableWithRetryAfter("db unavailable", 3*time.Second, nil)
	WriteServiceProblem(rec, req, logger, err)

	resp := rec.Result()
	// The RFC-compliant Retry-After header must be set, mirroring
	// WriteServiceError — not only the retry_after_seconds body extension.
	assert.Equal(t, "3", resp.Header.Get("Retry-After"))

	var p map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&p))
	assert.Equal(t, float64(3), p["retry_after_seconds"])
}

func TestWriteServiceProblem_SetsRetryAfterHeaderForRateLimit(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	err := apperror.NewRateLimitWithRetryAfter("too many", 2500*time.Millisecond)
	WriteServiceProblem(rec, req, logger, err)

	// 2.5s rounds up to 3 per math.Ceil.
	assert.Equal(t, "3", rec.Result().Header.Get("Retry-After"))
}

func TestWriteServiceProblem_NilLogger_LogsServerError(t *testing.T) {
	// With a nil logger, WriteServiceProblem must still log 5xx errors by
	// falling back to the request-scoped logger / slog.Default, rather than
	// dropping them silently.
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	WriteServiceProblem(rec, req, nil, apperror.NewOperationFailed("boom"))

	if buf.Len() == 0 {
		t.Fatal("expected operation-failed error to be logged with a nil logger")
	}
	assert.Contains(t, buf.String(), "operation failed")
}

func TestWriteServiceProblem_DoesNotLeakGenericErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))

	WriteServiceProblem(rec, req, logger,
		errors.New("pq: password authentication failed for postgres://user:secret@10.0.0.5/app"))

	var p map[string]any
	require.NoError(t, json.NewDecoder(rec.Result().Body).Decode(&p))
	assert.Equal(t, "internal error", p["detail"])
	assert.NotContains(t, p["detail"], "10.0.0.5")
	assert.NotContains(t, p["detail"], "secret")
}
