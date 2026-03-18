package cors

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCORS_AllowAll(t *testing.T) {
	t.Parallel()

	handler := New(Options{AllowedOrigins: []string{"*"}})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_SpecificOrigin(t *testing.T) {
	t.Parallel()

	handler := New(Options{AllowedOrigins: []string{"https://example.com"}})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("allowed origin", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Origin", "https://example.com")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		assert.Equal(t, "https://example.com", w.Header().Get("Access-Control-Allow-Origin"))
		assert.Contains(t, w.Header().Values("Vary"), "Origin")
	})

	t.Run("disallowed origin", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Origin", "https://evil.com")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	})
}

func TestCORS_Preflight(t *testing.T) {
	t.Parallel()

	handler := New(Options{AllowedOrigins: []string{"https://example.com"}})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler should not be called for preflight")
	}))

	r := httptest.NewRequest("OPTIONS", "/", nil)
	r.Header.Set("Origin", "https://example.com")
	r.Header.Set("Access-Control-Request-Method", "PUT")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusNoContent, w.Code)
	// jub0bs echoes the requested method per the Fetch spec.
	assert.Equal(t, "PUT", w.Header().Get("Access-Control-Allow-Methods"))
	assert.Equal(t, "86400", w.Header().Get("Access-Control-Max-Age"))
}

func TestCORS_NoOriginHeader(t *testing.T) {
	t.Parallel()

	called := false
	handler := New(Options{AllowedOrigins: []string{"https://example.com"}})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.True(t, called)
	// Non-CORS requests (no Origin header) should not get CORS headers.
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_AllowCredentials(t *testing.T) {
	t.Parallel()

	handler := New(Options{
		AllowedOrigins:   []string{"https://example.com"},
		AllowCredentials: true,
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, "true", w.Header().Get("Access-Control-Allow-Credentials"))
}

func TestCORS_WildcardWithCredentials_Panics(t *testing.T) {
	t.Parallel()

	assert.Panics(t, func() {
		New(Options{
			AllowedOrigins:   []string{"*"},
			AllowCredentials: true,
		})
	})
}

func TestCORS_MixedWildcardWithCredentials_Panics(t *testing.T) {
	t.Parallel()

	assert.Panics(t, func() {
		New(Options{
			AllowedOrigins:   []string{"https://example.com", "*"},
			AllowCredentials: true,
		})
	})
}

func TestCORS_ExposedHeaders(t *testing.T) {
	t.Parallel()

	handler := New(Options{
		AllowedOrigins: []string{"https://example.com"},
		ExposedHeaders: []string{"X-Request-Id", "X-Trace-Id"},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	exposed := w.Header().Get("Access-Control-Expose-Headers")
	assert.Contains(t, exposed, "x-request-id")
	assert.Contains(t, exposed, "x-trace-id")
}

func TestCORS_SubdomainWildcard(t *testing.T) {
	t.Parallel()

	handler := New(Options{
		AllowedOrigins: []string{"https://*.example.com"},
	})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("subdomain allowed", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Origin", "https://app.example.com")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		assert.Equal(t, "https://app.example.com", w.Header().Get("Access-Control-Allow-Origin"))
	})

	t.Run("different domain rejected", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Origin", "https://evil.com")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	})
}
