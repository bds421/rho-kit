package cors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCORS_AllowAll(t *testing.T) {
	t.Parallel()

	handler := New(WithAllowedOrigins("*"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, r)

	assert.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_PanicsWithoutExplicitAllowedOrigins(t *testing.T) {
	t.Parallel()

	cases := [][]Option{
		nil,
		{WithAllowedOrigins()},
		{WithAllowedOrigins("", " \t ")},
	}
	for _, opts := range cases {
		opts := opts
		assert.Panics(t, func() {
			New(opts...)
		})
	}
}

func TestCORS_SpecificOrigin(t *testing.T) {
	t.Parallel()

	handler := New(WithAllowedOrigins("https://example.com"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	handler := New(WithAllowedOrigins("https://example.com"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	handler := New(WithAllowedOrigins("https://example.com"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestCORS_RejectsDuplicateOriginHeaders(t *testing.T) {
	t.Parallel()

	called := false
	handler := New(WithAllowedOrigins("https://example.com"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Add("Origin", "https://example.com")
	r.Header.Add("Origin", "https://evil.com")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.False(t, called)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	assertJSONError(t, w, "invalid CORS request")
}

func TestCORS_RejectsBlankOriginHeader(t *testing.T) {
	t.Parallel()

	called := false
	handler := New(WithAllowedOrigins("https://example.com"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Origin", " \t ")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.False(t, called)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_RejectsInjectedOriginHeader(t *testing.T) {
	t.Parallel()

	called := false
	handler := New(WithAllowedOrigins("https://example.com"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Origin", "https://example.com\r\nX-Evil: injected")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.False(t, called)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, w.Header().Values("X-Evil"))
}

func TestCORS_RejectsInvalidOriginHeaderValue(t *testing.T) {
	t.Parallel()

	called := false
	handler := New(WithAllowedOrigins("https://example.com"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Origin", string([]byte{'h', 't', 't', 'p', 's', ':', '/', '/', 0xff}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.False(t, called)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
}

func TestCORS_RejectsInjectedPreflightRequestHeaders(t *testing.T) {
	t.Parallel()

	called := false
	handler := New(WithAllowedOrigins("https://example.com"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("OPTIONS", "/", nil)
	r.Header.Set("Origin", "https://example.com")
	r.Header.Set("Access-Control-Request-Method", "PUT")
	r.Header.Set("Access-Control-Request-Headers", "Content-Type\r\nX-Evil: injected")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.False(t, called)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Headers"))
	assert.Empty(t, w.Header().Values("X-Evil"))
}

func TestCORS_RejectsDuplicatePreflightMethodHeaders(t *testing.T) {
	t.Parallel()

	called := false
	handler := New(WithAllowedOrigins("https://example.com"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("OPTIONS", "/", nil)
	r.Header.Set("Origin", "https://example.com")
	r.Header.Add("Access-Control-Request-Method", "PUT")
	r.Header.Add("Access-Control-Request-Method", "DELETE")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.False(t, called)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Methods"))
}

func TestCORS_RejectsDuplicatePreflightRequestHeaders(t *testing.T) {
	t.Parallel()

	called := false
	handler := New(WithAllowedOrigins("https://example.com"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("OPTIONS", "/", nil)
	r.Header.Set("Origin", "https://example.com")
	r.Header.Set("Access-Control-Request-Method", "PUT")
	r.Header.Add("Access-Control-Request-Headers", "Content-Type")
	r.Header.Add("Access-Control-Request-Headers", "Authorization")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.False(t, called)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Headers"))
}

func TestCORS_RejectsBlankPreflightRequestHeaders(t *testing.T) {
	t.Parallel()

	called := false
	handler := New(WithAllowedOrigins("https://example.com"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	r := httptest.NewRequest("OPTIONS", "/", nil)
	r.Header.Set("Origin", "https://example.com")
	r.Header.Set("Access-Control-Request-Method", "PUT")
	r.Header.Set("Access-Control-Request-Headers", " \t ")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.False(t, called)
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Origin"))
	assert.Empty(t, w.Header().Get("Access-Control-Allow-Headers"))
}

func TestCORS_AllowCredentials(t *testing.T) {
	t.Parallel()

	handler := New(
		WithAllowedOrigins("https://example.com"),
		WithCredentials(),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	assert.PanicsWithValue(t, "cors: invalid configuration", func() {
		New(WithAllowedOrigins("*"), WithCredentials())
	})
}

func TestCORS_MixedWildcardWithCredentials_Panics(t *testing.T) {
	t.Parallel()

	assert.Panics(t, func() {
		New(WithAllowedOrigins("https://example.com", "*"), WithCredentials())
	})
}

func TestCORS_ExposedHeaders(t *testing.T) {
	t.Parallel()

	handler := New(
		WithAllowedOrigins("https://example.com"),
		WithExposedHeaders("X-Request-Id", "X-Trace-Id"),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestCORS_OptionsSlicesAreDetached(t *testing.T) {
	t.Parallel()

	origins := []string{"https://example.com"}
	methods := []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"}
	requestHeaders := []string{"X-Allowed"}
	exposedHeaders := []string{"X-Expose"}
	handler := New(
		WithAllowedOrigins(origins...),
		WithAllowedMethods(methods...),
		WithAllowedHeaders(requestHeaders...),
		WithExposedHeaders(exposedHeaders...),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	origins[0] = "https://evil.example"
	methods[2] = "PATCH"
	requestHeaders[0] = "X-Evil"
	exposedHeaders[0] = "X-Evil-Expose"

	getReq := httptest.NewRequest("GET", "/", nil)
	getReq.Header.Set("Origin", "https://example.com")
	getRec := httptest.NewRecorder()
	handler.ServeHTTP(getRec, getReq)
	assert.Equal(t, "https://example.com", getRec.Header().Get("Access-Control-Allow-Origin"))
	assert.Contains(t, getRec.Header().Get("Access-Control-Expose-Headers"), "x-expose")
	assert.NotContains(t, getRec.Header().Get("Access-Control-Expose-Headers"), "x-evil-expose")

	preflightReq := httptest.NewRequest("OPTIONS", "/", nil)
	preflightReq.Header.Set("Origin", "https://example.com")
	preflightReq.Header.Set("Access-Control-Request-Method", "PUT")
	preflightReq.Header.Set("Access-Control-Request-Headers", "x-allowed")
	preflightRec := httptest.NewRecorder()
	handler.ServeHTTP(preflightRec, preflightReq)
	assert.Equal(t, http.StatusNoContent, preflightRec.Code)
	assert.Equal(t, "PUT", preflightRec.Header().Get("Access-Control-Allow-Methods"))
	assert.Contains(t, preflightRec.Header().Get("Access-Control-Allow-Headers"), "x-allowed")
	assert.NotContains(t, preflightRec.Header().Get("Access-Control-Allow-Headers"), "x-evil")
}

func assertJSONError(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()

	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	var body struct {
		Error string `json:"error"`
		Code  string `json:"code"`
	}
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, want, body.Error)
	assert.NotEmpty(t, body.Code)
}

func TestCORS_SubdomainWildcard(t *testing.T) {
	t.Parallel()

	handler := New(WithAllowedOrigins("https://*.example.com"))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
