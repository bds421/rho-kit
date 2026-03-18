package csrf

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Double-submit cookie CSRF (csrf.New) tests ---

func testSecret() []byte {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	return secret
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestNew_SetsCookieOnGET(t *testing.T) {
	mw := New(WithSecret(testSecret()))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, "__csrf", cookies[0].Name)
	assert.NotEmpty(t, cookies[0].Value)
}

func TestNew_GETPassesWithoutHeader(t *testing.T) {
	mw := New(WithSecret(testSecret()))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNew_POSTRejectedWithoutCSRFCookie(t *testing.T) {
	mw := New(WithSecret(testSecret()))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestNew_POSTRejectedWithoutHeader(t *testing.T) {
	secret := testSecret()
	mw := New(WithSecret(secret))
	handler := mw(okHandler())

	token := generateSignedToken(secret)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestNew_POSTSucceedsWithValidToken(t *testing.T) {
	secret := testSecret()
	mw := New(WithSecret(secret))
	handler := mw(okHandler())

	token := generateSignedToken(secret)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: token})
	req.Header.Set("X-CSRF-Token", token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNew_POSTRejectedWithMismatchedToken(t *testing.T) {
	secret := testSecret()
	mw := New(WithSecret(secret))
	handler := mw(okHandler())

	token1 := generateSignedToken(secret)
	token2 := generateSignedToken(secret)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: token1})
	req.Header.Set("X-CSRF-Token", token2)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestNew_POSTRejectedWithForgedToken(t *testing.T) {
	secret := testSecret()
	mw := New(WithSecret(secret))
	handler := mw(okHandler())

	forged := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.forgedsig"
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: forged})
	req.Header.Set("X-CSRF-Token", forged)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestNew_AllStateChangingMethodsRequireToken(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			mw := New(WithSecret(testSecret()))
			handler := mw(okHandler())

			req := httptest.NewRequest(method, "/", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusForbidden, rec.Code)
		})
	}
}

func TestNew_SafeMethodsPass(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			mw := New(WithSecret(testSecret()))
			handler := mw(okHandler())

			req := httptest.NewRequest(method, "/", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

func TestNew_CustomNames(t *testing.T) {
	secret := testSecret()
	mw := New(WithSecret(secret), WithCookieName("_xsrf"), WithHeaderName("X-Custom"))
	handler := mw(okHandler())

	token := generateSignedToken(secret)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "_xsrf", Value: token})
	req.Header.Set("X-Custom", token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNew_SecureFlag(t *testing.T) {
	mw := New(WithSecret(testSecret()), WithSecure(true))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.True(t, cookies[0].Secure)
}

func TestNew_PanicsOnShortSecret(t *testing.T) {
	assert.Panics(t, func() {
		New(WithSecret([]byte("tooshort")))
	})
}

func TestNew_TokenFromDifferentSecretRejected(t *testing.T) {
	secret1 := testSecret()
	secret2 := make([]byte, 32)
	for i := range secret2 {
		secret2[i] = byte(i + 100)
	}

	mw := New(WithSecret(secret1))
	handler := mw(okHandler())

	// Token signed with a different secret.
	token := generateSignedToken(secret2)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: token})
	req.Header.Set("X-CSRF-Token", token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestIsValidSignedToken_InvalidFormats(t *testing.T) {
	secret := testSecret()
	assert.False(t, isValidSignedToken("", secret))
	assert.False(t, isValidSignedToken("noseparator", secret))
	assert.False(t, isValidSignedToken(".onlysuffix", secret))
	assert.False(t, isValidSignedToken("onlyprefix.", secret))
}

// --- Legacy RequireCSRF tests ---

func TestRequireCSRF_GET_NoHeader(t *testing.T) {
	handler := RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET without header should pass, got %d", rec.Code)
	}
}

func TestRequireCSRF_HEAD_NoHeader(t *testing.T) {
	handler := RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodHead, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD without header should pass, got %d", rec.Code)
	}
}

func TestRequireCSRF_OPTIONS_NoHeader(t *testing.T) {
	handler := RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("OPTIONS without header should pass, got %d", rec.Code)
	}
}

func TestRequireCSRF_POST_WithoutHeader(t *testing.T) {
	called := false
	handler := RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without X-Requested-With should be 403, got %d", rec.Code)
	}
	if called {
		t.Error("next handler should not be called")
	}
}

func TestRequireCSRF_POST_WithHeader(t *testing.T) {
	handler := RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST with X-Requested-With should pass, got %d", rec.Code)
	}
}

// --- RequireJSONContentType tests ---

func TestRequireJSONContentType_POST_WithJSON(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST with JSON Content-Type should pass, got %d", rec.Code)
	}
}

func TestRequireJSONContentType_POST_WithCharset(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST with JSON+charset should pass, got %d", rec.Code)
	}
}

func TestRequireJSONContentType_POST_WithoutJSON(t *testing.T) {
	called := false
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("POST with non-JSON should be 415, got %d", rec.Code)
	}
	if called {
		t.Error("next handler should not be called")
	}
}

func TestRequireJSONContentType_POST_NoContentType(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("POST without Content-Type should be 415, got %d", rec.Code)
	}
}

func TestRequireJSONContentType_PUT_WithJSON(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPut, "/", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT with JSON should pass, got %d", rec.Code)
	}
}

func TestRequireJSONContentType_PATCH_WithoutJSON(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPatch, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("PATCH without JSON should be 415, got %d", rec.Code)
	}
}

func TestRequireJSONContentType_DELETE_NoBody(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE without body should pass, got %d", rec.Code)
	}
}

func TestRequireJSONContentType_DELETE_WithBodyNoJSON(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	req.ContentLength = 10
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("DELETE with body and non-JSON should be 415, got %d", rec.Code)
	}
}

func TestRequireJSONContentType_DELETE_WithBodyJSON(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	req.ContentLength = 10
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE with body and JSON should pass, got %d", rec.Code)
	}
}

func TestRequireJSONContentType_GET_NoContentType(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET should pass regardless of Content-Type, got %d", rec.Code)
	}
}

func TestRequireJSONContentType_HEAD_Passes(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodHead, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD should pass, got %d", rec.Code)
	}
}

func TestRequireJSONContentType_OPTIONS_Passes(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodOptions, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("OPTIONS should pass, got %d", rec.Code)
	}
}

func TestRequireCSRF_StateChangingMethods(t *testing.T) {
	methods := []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

	for _, method := range methods {
		t.Run(method+"_blocked", func(t *testing.T) {
			handler := RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(method, "/", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s without header should be 403, got %d", method, rec.Code)
			}
		})

		t.Run(method+"_allowed", func(t *testing.T) {
			handler := RequireCSRF(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(method, "/", nil)
			req.Header.Set("X-Requested-With", "fetch")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("%s with header should pass, got %d", method, rec.Code)
			}
		})
	}
}
