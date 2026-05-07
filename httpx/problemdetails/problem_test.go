package problemdetails

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/apperror"
)

func TestProblem_MarshalJSON_OmitsEmpty(t *testing.T) {
	p := Problem{Title: "Bad", Status: 400}
	b, err := json.Marshal(p)
	require.NoError(t, err)
	got := string(b)
	assert.Contains(t, got, `"title":"Bad"`)
	assert.Contains(t, got, `"status":400`)
	assert.NotContains(t, got, `"type"`)
	assert.NotContains(t, got, `"detail"`)
	assert.NotContains(t, got, `"instance"`)
}

func TestProblem_MarshalJSON_InlinesExtensions(t *testing.T) {
	p := Problem{
		Type:   "https://example.com/errors/oops",
		Title:  "Oops",
		Status: 400,
		Extensions: map[string]any{
			"correlation_id": "abc123",
			"retry_after":    30,
		},
	}
	b, err := json.Marshal(p)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(b, &decoded))
	assert.Equal(t, "https://example.com/errors/oops", decoded["type"])
	assert.Equal(t, "abc123", decoded["correlation_id"])
	assert.Equal(t, float64(30), decoded["retry_after"])
}

func TestProblem_MarshalJSON_RejectsReservedExtensionKey(t *testing.T) {
	p := Problem{
		Title: "x",
		Extensions: map[string]any{
			"status": 999, // collides with reserved member
		},
	}
	_, err := json.Marshal(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reserved")
}

func TestWrite_SetsContentTypeAndStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	Write(rr, Problem{Status: http.StatusBadRequest, Title: "Bad"})

	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Equal(t, ContentType, rr.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rr.Header().Get("Cache-Control"))

	var p Problem
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&p))
	assert.Equal(t, "Bad", p.Title)
	assert.Equal(t, http.StatusBadRequest, p.Status)
}

func TestWrite_DefaultsTo500(t *testing.T) {
	rr := httptest.NewRecorder()
	Write(rr, Problem{}) // no status set
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
}

func TestFromError_NotFound(t *testing.T) {
	p := FromError(apperror.NewNotFound("user", 42))
	assert.Equal(t, http.StatusNotFound, p.Status)
	assert.Equal(t, "Not Found", p.Title)
	assert.Equal(t, "about:blank", p.Type)
	assert.Contains(t, p.Detail, "user")
}

func TestFromError_RateLimit_AddsRetryAfterExtension(t *testing.T) {
	p := FromError(apperror.NewRateLimit("slow down", 30*time.Second))
	assert.Equal(t, http.StatusTooManyRequests, p.Status)
	require.NotNil(t, p.Extensions)
	assert.Equal(t, 30, p.Extensions["retry_after_seconds"])
}

func TestFromError_DependencyUnavailable(t *testing.T) {
	p := FromError(apperror.NewDependencyUnavailable("redis", "down", nil))
	assert.Equal(t, http.StatusBadGateway, p.Status)
}

func TestFromError_UnavailableSelfReturns503(t *testing.T) {
	p := FromError(apperror.NewUnavailable("not ready"))
	assert.Equal(t, http.StatusServiceUnavailable, p.Status)
}

func TestFromError_GenericErrorReturns500(t *testing.T) {
	p := FromError(errors.New("unexpected"))
	assert.Equal(t, http.StatusInternalServerError, p.Status)
}

func TestFromError_WithBaseURLProducesTypeURI(t *testing.T) {
	p := FromError(apperror.NewNotFound("user", 1), WithBaseURL("https://errors.example.com"))
	// apperror.Code values are SCREAMING_SNAKE so URIs read as
	// https://errors.example.com/NOT_FOUND. Operators map these to docs.
	assert.Equal(t, "https://errors.example.com/"+string(apperror.CodeNotFound), p.Type)
}

func TestFromError_WithInstance(t *testing.T) {
	p := FromError(apperror.NewNotFound("user", 1), WithInstance("/users/1"))
	assert.Equal(t, "/users/1", p.Instance)
}

func TestFromError_FieldValidation(t *testing.T) {
	err := apperror.NewFieldValidation(
		apperror.FieldError{Field: "name", Message: "is required"},
		apperror.FieldError{Field: "age", Message: "must be ≥ 0"},
	)
	p := FromError(err)
	assert.Equal(t, http.StatusBadRequest, p.Status)
	require.NotNil(t, p.Extensions)

	// Round-trip through JSON to inspect the extension structure.
	b, marshalErr := json.Marshal(p)
	require.NoError(t, marshalErr)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(b, &decoded))

	errs, ok := decoded["errors"].([]any)
	require.True(t, ok, "errors extension must be a JSON array")
	require.Len(t, errs, 2)

	first := errs[0].(map[string]any)
	assert.Equal(t, "name", first["field"])
	assert.Equal(t, "is required", first["message"])
}

func TestWrite_WithFromError_EndToEnd(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Write(w, FromError(
			apperror.NewRateLimit("too fast", 5*time.Second),
			WithBaseURL("https://errors.example.com"),
			WithInstance(r.URL.RequestURI()),
		))
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/things?limit=1")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, ContentType, resp.Header.Get("Content-Type"))

	var decoded map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&decoded))
	assert.Equal(t, "https://errors.example.com/"+string(apperror.CodeRateLimit), decoded["type"])
	assert.Equal(t, "/api/things?limit=1", decoded["instance"])
	assert.Equal(t, float64(5), decoded["retry_after_seconds"])
}
