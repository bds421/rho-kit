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

	"github.com/bds421/rho-kit/core/v2/apperror"
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
	assert.NotContains(t, err.Error(), "status")
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

func TestWrite_MarshalErrorDoesNotLeakRawDetails(t *testing.T) {
	rr := httptest.NewRecorder()
	Write(rr, Problem{
		Status: http.StatusBadRequest,
		Extensions: map[string]any{
			"secret": make(chan int),
		},
	})

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Equal(t, ContentType, rr.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rr.Header().Get("Cache-Control"))
	assert.NotContains(t, rr.Body.String(), "unsupported type")
	assert.NotContains(t, rr.Body.String(), "chan")
	assert.NotContains(t, rr.Body.String(), "secret")

	var decoded map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&decoded))
	assert.Equal(t, "Internal Server Error", decoded["title"])
	assert.Equal(t, float64(http.StatusInternalServerError), decoded["status"])
	assert.Equal(t, "internal error", decoded["detail"])
}

func TestWrite_ReservedExtensionKeyDoesNotLeakRawDetails(t *testing.T) {
	rr := httptest.NewRecorder()
	Write(rr, Problem{
		Status: http.StatusBadRequest,
		Extensions: map[string]any{
			"status": "client-controlled collision",
		},
	})

	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Equal(t, ContentType, rr.Header().Get("Content-Type"))
	assert.NotContains(t, rr.Body.String(), "reserved")
	assert.NotContains(t, rr.Body.String(), "client-controlled")
}

func TestFromError_NotFound(t *testing.T) {
	p := FromError(apperror.NewNotFound("user", 42))
	assert.Equal(t, http.StatusNotFound, p.Status)
	assert.Equal(t, "Not Found", p.Title)
	assert.Equal(t, "about:blank", p.Type)
	assert.Equal(t, "resource not found", p.Detail)
}

func TestFromError_RateLimit_AddsRetryAfterExtension(t *testing.T) {
	p := FromError(apperror.NewRateLimitWithRetryAfter("slow down", 30*time.Second))
	assert.Equal(t, http.StatusTooManyRequests, p.Status)
	require.NotNil(t, p.Extensions)
	assert.Equal(t, 30, p.Extensions["retry_after_seconds"])
}

func TestFromError_DependencyUnavailable(t *testing.T) {
	p := FromError(apperror.NewDependencyUnavailable(
		"redis",
		"dial tcp 10.0.0.5:6379: connection refused",
		errors.New("redis password auth failed for redis://:secret@10.0.0.5:6379"),
	))
	assert.Equal(t, http.StatusBadGateway, p.Status)
	assert.Equal(t, "service unavailable", p.Detail)
	assert.NotContains(t, p.Detail, "10.0.0.5")
	assert.NotContains(t, p.Detail, "secret")
}

func TestFromError_UnavailableSelfReturns503(t *testing.T) {
	p := FromError(apperror.NewUnavailable("postgres://user:secret@10.0.0.5/app is not ready"))
	assert.Equal(t, http.StatusServiceUnavailable, p.Status)
	assert.Equal(t, "service unavailable", p.Detail)
	assert.NotContains(t, p.Detail, "10.0.0.5")
	assert.NotContains(t, p.Detail, "secret")
}

func TestFromError_GenericErrorReturns500(t *testing.T) {
	p := FromError(errors.New("pq: password authentication failed for postgres://user:secret@10.0.0.5/app"))
	assert.Equal(t, http.StatusInternalServerError, p.Status)
	assert.Equal(t, "internal error", p.Detail)
	assert.NotContains(t, p.Detail, "10.0.0.5")
	assert.NotContains(t, p.Detail, "secret")
}

func TestSafeDetail(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "nil", want: "internal error"},
		{name: "validation", err: apperror.NewValidation("email is required"), want: "email is required"},
		{name: "rate limit", err: apperror.NewRateLimit("tenant quota exhausted"), want: "rate limit exceeded"},
		{name: "not found", err: apperror.NewNotFound("user", 42), want: "resource not found"},
		{name: "conflict", err: apperror.NewConflict("unique index users_email_key"), want: "resource conflict"},
		{name: "permanent", err: apperror.NewPermanent("invalid irreversible state: order row 42"), want: "operation cannot be completed"},
		{name: "auth required", err: apperror.NewAuthRequired("jwt parse failed: bad signature"), want: "authentication required"},
		{name: "forbidden", err: apperror.NewForbidden("policy tuple missing"), want: "forbidden"},
		{name: "unavailable", err: apperror.NewUnavailable("redis at 10.0.0.5 down"), want: "service unavailable"},
		{name: "operation failed", err: apperror.NewOperationFailed("payment failed for postgres://user:secret@10.0.0.5/app"), want: "internal error"},
		{name: "generic", err: errors.New("dial tcp 10.0.0.5:5432"), want: "internal error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, SafeDetail(tt.err))
		})
	}
}

func TestFromError_WithBaseURLProducesTypeURI(t *testing.T) {
	p := FromError(apperror.NewNotFound("user", 1), WithBaseURL("https://errors.example.com"))
	// apperror.Code values are SCREAMING_SNAKE so URIs read as
	// https://errors.example.com/NOT_FOUND. Operators map these to docs.
	assert.Equal(t, "https://errors.example.com/"+string(apperror.CodeNotFound), p.Type)
}

func TestFromError_WithBaseURLTrimsTrailingSlash(t *testing.T) {
	p := FromError(apperror.NewNotFound("user", 1), WithBaseURL("https://errors.example.com/docs/"))
	assert.Equal(t, "https://errors.example.com/docs/"+string(apperror.CodeNotFound), p.Type)
}

func TestFromError_WithBaseURLPanicsOnInvalidBase(t *testing.T) {
	tests := []string{
		" errors.example.com",
		"errors.example.com",
		"javascript:alert(1)",
		"https://user:pass@errors.example.com",
		"https://errors.example.com:0",
		"https://errors.example.com:65536",
		"https://[fe80::1%25lo0]",
		"https://errors.example.com?tenant=acme",
		"https://errors.example.com#types",
	}
	for _, base := range tests {
		t.Run(base, func(t *testing.T) {
			assert.Panics(t, func() {
				_ = FromError(apperror.NewNotFound("user", 1), WithBaseURL(base))
			})
		})
	}
}

func TestFromError_WithBaseURLParseErrorDoesNotEchoValue(t *testing.T) {
	require.PanicsWithValue(t, "problemdetails: base URL is invalid", func() {
		_ = FromError(apperror.NewNotFound("user", 1), WithBaseURL("https://errors.example.com/%zz?token=secret-token"))
	})
}

func TestFromError_WithBaseURLSchemeErrorDoesNotEchoValue(t *testing.T) {
	require.PanicsWithValue(t, "problemdetails: base URL is invalid", func() {
		_ = FromError(apperror.NewNotFound("user", 1), WithBaseURL("ftp://secret-token.example"))
	})
}

func TestFromError_NilErrorReturnsGenericProblem(t *testing.T) {
	p := FromError(nil)
	assert.Equal(t, http.StatusInternalServerError, p.Status)
	assert.Equal(t, "Internal Server Error", p.Title)
	assert.Equal(t, "internal error", p.Detail)
}

func TestFromError_PanicsOnNilOption(t *testing.T) {
	assert.Panics(t, func() {
		FromError(apperror.NewNotFound("user", 1), nil)
	})
}

func TestFromError_WithInstance(t *testing.T) {
	p := FromError(apperror.NewNotFound("user", 1), WithInstance("/users/1"))
	assert.Equal(t, "/users/1", p.Instance)
}

func TestFromError_WithInstancePanicsOnUnsafeInstance(t *testing.T) {
	tests := []string{
		"users/1",
		"//evil.example/path",
		"/users/1?token=secret",
		"/users/1#frag",
		"/users\\1",
		" /users/1",
		"/users 1",
		"/users\n1",
		"/users/%zz",
		string([]byte{'/', 'u', 0xff}),
	}
	for _, instance := range tests {
		t.Run(instance, func(t *testing.T) {
			assert.Panics(t, func() {
				_ = FromError(apperror.NewNotFound("user", 1), WithInstance(instance))
			})
		})
	}
}

func TestFromError_WithInstanceEscapeErrorDoesNotEchoValue(t *testing.T) {
	require.PanicsWithValue(t, "problemdetails: instance path is invalid", func() {
		_ = FromError(apperror.NewNotFound("user", 1), WithInstance("/users/%zz/secret-token"))
	})
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
			apperror.NewRateLimitWithRetryAfter("too fast", 5*time.Second),
			WithBaseURL("https://errors.example.com"),
			WithInstance(r.URL.EscapedPath()),
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
	assert.Equal(t, "/api/things", decoded["instance"])
	assert.NotContains(t, decoded["instance"], "limit=1")
	assert.Equal(t, float64(5), decoded["retry_after_seconds"])
}
