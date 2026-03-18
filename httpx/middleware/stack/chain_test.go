package stack

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func addHeader(name, value string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add(name, value)
			next.ServeHTTP(w, r)
		})
	}
}

func TestChain_Then(t *testing.T) {
	chain := NewChain(
		addHeader("X-Order", "first"),
		addHeader("X-Order", "second"),
	)

	handler := chain.Then(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	// First middleware is outermost, so its header is added first
	values := rec.Header().Values("X-Order")
	assert.Equal(t, []string{"first", "second"}, values)
}

func TestChain_Append(t *testing.T) {
	base := NewChain(addHeader("X-Order", "first"))
	extended := base.Append(addHeader("X-Order", "second"))

	handler := extended.Then(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	values := rec.Header().Values("X-Order")
	assert.Equal(t, []string{"first", "second"}, values)

	// Original chain is not modified
	assert.Equal(t, 1, base.Len())
	assert.Equal(t, 2, extended.Len())
}

func TestChain_ThenFunc(t *testing.T) {
	chain := NewChain(addHeader("X-Test", "yes"))

	handler := chain.ThenFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	assert.Equal(t, "yes", rec.Header().Get("X-Test"))
}

func TestChain_Empty(t *testing.T) {
	chain := NewChain()

	called := false
	handler := chain.Then(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))

	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestChain_Len(t *testing.T) {
	assert.Equal(t, 0, NewChain().Len())
	assert.Equal(t, 2, NewChain(addHeader("a", "b"), addHeader("c", "d")).Len())
}

func TestChain_Immutable(t *testing.T) {
	chain := NewChain(addHeader("X-A", "1"))
	chain.Append(addHeader("X-B", "2"))

	// Original should not be modified
	assert.Equal(t, 1, chain.Len())
}
