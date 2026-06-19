package openapi_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/httpx/v2/openapi"
	"github.com/bds421/rho-kit/httpx/v2/problemdetails"
)

func TestMount_PassesThroughHandler(t *testing.T) {
	called := false
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})

	mounted := openapi.Mount(h, openapi.Options{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	mounted.ServeHTTP(rec, req)

	require.True(t, called, "the wrapped handler must run")
	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestMount_RecoversFromHandlerPanic(t *testing.T) {
	h := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		panic("intentional")
	})

	mounted := openapi.Mount(h, openapi.Options{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	mounted.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code,
		"recover middleware must convert panic to 500")
}

func TestDefaultErrorMapper_NilReturnsOK(t *testing.T) {
	p := openapi.DefaultErrorMapper(nil)
	assert.Equal(t, http.StatusOK, p.Status)
}

func TestDefaultErrorMapper_ErrorReturns500(t *testing.T) {
	p := openapi.DefaultErrorMapper(errors.New("pq: password authentication failed for postgres://user:secret@10.0.0.5/app"))
	assert.Equal(t, http.StatusInternalServerError, p.Status)
	assert.Equal(t, "internal error", p.Detail)
	assert.NotContains(t, p.Detail, "secret")
	assert.NotContains(t, p.Detail, "10.0.0.5")
}

func TestDefaultErrorMapper_ApperrorUsesProblemDetailsMapping(t *testing.T) {
	p := openapi.DefaultErrorMapper(apperror.NewNotFound("widget", "secret-id"))
	assert.Equal(t, http.StatusNotFound, p.Status)
	assert.Equal(t, "resource not found", p.Detail)
	assert.NotContains(t, p.Detail, "secret-id")
}

func TestStrictErrorHandler_WritesProblemDetailsFromDefaultMapper(t *testing.T) {
	h := openapi.StrictErrorHandler(nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/widgets/secret-id", nil)

	h(rec, req, apperror.NewNotFound("widget", "secret-id"))

	assert.Equal(t, http.StatusNotFound, rec.Code,
		"strict error handler must map the apperror to its HTTP status")
	assert.Equal(t, problemdetails.ContentType, rec.Header().Get("Content-Type"),
		"strict error handler must emit RFC 7807 problem+json")
	body := rec.Body.String()
	assert.Contains(t, body, "resource not found")
	assert.NotContains(t, body, "secret-id",
		"the safe detail must not leak the resource id")
}

func TestStrictErrorHandler_UsesCustomMapper(t *testing.T) {
	custom := func(error) problemdetails.Problem {
		return problemdetails.Problem{Status: http.StatusTeapot, Detail: "brewing"}
	}
	h := openapi.StrictErrorHandler(custom)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	h(rec, req, errors.New("boom"))

	assert.Equal(t, http.StatusTeapot, rec.Code,
		"custom mapper status must drive the response")
	assert.Contains(t, rec.Body.String(), "brewing")
}
