package csrf

import (
	"net/http"
	"net/http/httptest"
	"strings"
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

// --- WithSkipCheck tests ---

func TestNew_SkipCheck_POSTWithBearerTokenSkipsCSRF(t *testing.T) {
	mw := New(WithSecret(testSecret()), WithSkipCheck(HasBearerToken))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer some-jwt-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNew_SkipCheck_POSTWithoutBearerTokenRequiresCSRF(t *testing.T) {
	mw := New(WithSecret(testSecret()), WithSkipCheck(HasBearerToken))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestNew_SkipCheck_POSTWithAPIKeySkipsCSRF(t *testing.T) {
	mw := New(WithSecret(testSecret()), WithSkipCheck(HasAPIKey("X-API-Key")))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-API-Key", "my-api-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNew_SkipCheck_POSTWithCustomAPIKeyHeader(t *testing.T) {
	mw := New(WithSecret(testSecret()), WithSkipCheck(HasAPIKey("X-Custom-Auth")))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Custom-Auth", "secret-value")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHasAPIKey_PanicsOnEmptyHeader(t *testing.T) {
	assert.Panics(t, func() {
		HasAPIKey("")
	})
}

func TestNew_POSTWithBearerTokenNoSkipCheck_RequiresCSRF(t *testing.T) {
	mw := New(WithSecret(testSecret()))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer some-jwt-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestNew_SkipCheck_GETWithBearerTokenPasses(t *testing.T) {
	mw := New(WithSecret(testSecret()), WithSkipCheck(HasBearerToken))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer some-jwt-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNew_SkipCheck_CookieStillSetWhenSkipped(t *testing.T) {
	mw := New(WithSecret(testSecret()), WithSkipCheck(HasBearerToken))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer some-jwt-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, "__csrf", cookies[0].Name)
	assert.NotEmpty(t, cookies[0].Value)
}

func TestNew_SkipCheck_AllStateChangingMethodsWithBearerToken(t *testing.T) {
	methods := []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			mw := New(WithSecret(testSecret()), WithSkipCheck(HasBearerToken))
			handler := mw(okHandler())

			req := httptest.NewRequest(method, "/", nil)
			req.Header.Set("Authorization", "Bearer some-jwt-token")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
		})
	}
}

func TestHasBearerToken_CaseInsensitive(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{"canonical", "Bearer token123", true},
		{"lowercase", "bearer token123", true},
		{"uppercase", "BEARER token123", true},
		{"mixed", "bEaReR token123", true},
		{"empty", "", false},
		{"no space", "Bearertoken123", false},
		{"basic auth", "Basic dXNlcjpwYXNz", false},
		{"too short", "Bearer", false},
		{"space only", "Bearer ", false},
		{"lowercase space only", "bearer ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			assert.Equal(t, tt.want, HasBearerToken(req))
		})
	}
}

func TestNew_SkipCheck_BasicAuthDoesNotSkip(t *testing.T) {
	mw := New(WithSecret(testSecret()), WithSkipCheck(HasBearerToken))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestHasAPIKey_EmptyHeaderValue(t *testing.T) {
	predicate := HasAPIKey("X-API-Key")
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-API-Key", "")

	assert.False(t, predicate(req))
}

func TestHasAPIKey_WhitespaceOnlyHeaderValue(t *testing.T) {
	predicate := HasAPIKey("X-API-Key")
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-API-Key", "   \t  ")

	assert.False(t, predicate(req), "whitespace-only API key should be rejected")
}

func TestNew_SkipCheck_LastPredicateWins(t *testing.T) {
	// First predicate: always skip. Second predicate: never skip.
	// The last one should win.
	alwaysSkip := func(_ *http.Request) bool { return true }
	neverSkip := func(_ *http.Request) bool { return false }

	mw := New(
		WithSecret(testSecret()),
		WithSkipCheck(alwaysSkip),
		WithSkipCheck(neverSkip),
	)
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// neverSkip is the last predicate, so CSRF should be enforced and POST without token rejected.
	assert.Equal(t, http.StatusForbidden, rec.Code, "last predicate (neverSkip) should win")
}

func TestNew_SkipCheck_ComposedPredicates(t *testing.T) {
	// Compose HasBearerToken and HasAPIKey into a single predicate.
	mw := New(
		WithSecret(testSecret()),
		WithSkipCheck(func(r *http.Request) bool {
			return HasBearerToken(r) || HasAPIKey("X-API-Key")(r)
		}),
	)
	handler := mw(okHandler())

	t.Run("bearer token skips CSRF", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Authorization", "Bearer jwt-token")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("API key skips CSRF", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("X-API-Key", "my-key")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
	})

	t.Run("neither bearer nor API key enforces CSRF", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusForbidden, rec.Code)
	})
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

func TestRequireJSONContentType_POST_WithoutJSON_NoBody(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST with text/plain but no body should pass, got %d", rec.Code)
	}
}

func TestRequireJSONContentType_POST_NoContentType(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST without Content-Type (no body) should pass, got %d", rec.Code)
	}
}

func TestRequireJSONContentType_POST_WithBodyNoContentType(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"key":"value"}`))
	req.Header.Del("Content-Type")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("POST with body but no Content-Type should be 415, got %d", rec.Code)
	}
}

func TestRequireJSONContentType_POST_WrongContentType(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("data"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("POST with text/plain should be 415, got %d", rec.Code)
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

func TestRequireJSONContentType_PATCH_NoContentType(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPatch, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH without Content-Type (no body) should pass, got %d", rec.Code)
	}
}

func TestRequireJSONContentType_PATCH_WrongContentType(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader("data"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("PATCH with text/plain should be 415, got %d", rec.Code)
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

// --- WithAllowedOrigins ---

func TestNew_AllowedOrigins_AcceptsListedOrigin(t *testing.T) {
	secret := testSecret()
	mw := New(WithSecret(secret), WithAllowedOrigins("https://app.example.com"))
	handler := mw(okHandler())

	token := generateSignedToken(secret)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: token})
	req.Header.Set("X-CSRF-Token", token)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNew_AllowedOrigins_RejectsUnlistedOrigin(t *testing.T) {
	secret := testSecret()
	mw := New(WithSecret(secret), WithAllowedOrigins("https://app.example.com"))
	handler := mw(okHandler())

	token := generateSignedToken(secret)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: token})
	req.Header.Set("X-CSRF-Token", token)
	req.Header.Set("Origin", "https://evil.attacker.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "untrusted origin")
}

func TestNew_AllowedOrigins_RejectsMissingOriginAndReferer(t *testing.T) {
	secret := testSecret()
	mw := New(WithSecret(secret), WithAllowedOrigins("https://app.example.com"))
	handler := mw(okHandler())

	token := generateSignedToken(secret)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: token})
	req.Header.Set("X-CSRF-Token", token)
	// no Origin/Referer
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestNew_AllowedOrigins_FallsBackToReferer(t *testing.T) {
	secret := testSecret()
	mw := New(WithSecret(secret), WithAllowedOrigins("https://app.example.com"))
	handler := mw(okHandler())

	token := generateSignedToken(secret)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: token})
	req.Header.Set("X-CSRF-Token", token)
	req.Header.Set("Referer", "https://app.example.com/some/path?q=1")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNew_AllowedOrigins_SafeMethodsAreNotChecked(t *testing.T) {
	mw := New(WithSecret(testSecret()), WithAllowedOrigins("https://app.example.com"))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Origin", "https://evil.attacker.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// GET is exempt — origin allowlist only fires on state-changing methods.
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNew_AllowedOrigins_CaseInsensitiveHost(t *testing.T) {
	secret := testSecret()
	mw := New(WithSecret(secret), WithAllowedOrigins("https://App.Example.COM"))
	handler := mw(okHandler())

	token := generateSignedToken(secret)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: token})
	req.Header.Set("X-CSRF-Token", token)
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

// --- Mandatory secret in non-dev ---

func TestNew_PanicsWithoutSecretInProduction(t *testing.T) {
	t.Setenv("KIT_ENV", "production")
	t.Setenv("APP_ENV", "")
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when no secret is configured in non-dev")
		}
		assert.Contains(t, r.(string), "no HMAC secret configured")
	}()
	_ = New() // no WithSecret, no WithDevSecret
}

func TestNew_DevSecretFallbackInDev(t *testing.T) {
	t.Setenv("KIT_ENV", "development")
	t.Setenv("APP_ENV", "")
	mw := New() // no panic — dev allows per-process random fallback
	assert.NotNil(t, mw)
}

func TestNew_DevSecretExplicitOptIn(t *testing.T) {
	t.Setenv("KIT_ENV", "production") // would normally panic
	t.Setenv("APP_ENV", "")
	mw := New(WithDevSecret()) // explicit override
	assert.NotNil(t, mw)
}

func TestNew_PanicsOnSameSiteNoneWithoutSecure(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for SameSite=None without Secure")
		}
		assert.Contains(t, r.(string), "SameSite=None requires Secure=true")
	}()
	_ = New(WithSecret(testSecret()), WithSameSite(http.SameSiteNoneMode))
}

func TestNew_SameSiteNoneWithSecureIsAllowed(t *testing.T) {
	mw := New(WithSecret(testSecret()), WithSameSite(http.SameSiteNoneMode), WithSecure(true))
	assert.NotNil(t, mw)
}

// --- SkipCheck regen bug ---

func TestNew_RegeneratedCookieRejectsRequest(t *testing.T) {
	// Reproduce the audit's bug: an invalid cookie triggered regeneration,
	// but the same request was being compared against the (now-stale)
	// invalid cookie, returning a confusing 403 even though a fresh
	// cookie was just issued. The fix short-circuits with a clear 403
	// message that tells the client to retry.
	mw := New(WithSecret(testSecret()))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: "tampered-bogus-cookie"})
	req.Header.Set("X-CSRF-Token", "tampered-bogus-cookie")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "reissued")
	// Response carries the fresh cookie so the retry succeeds.
	assert.NotEmpty(t, rec.Header().Get("Set-Cookie"))
}
