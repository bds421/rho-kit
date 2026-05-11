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
