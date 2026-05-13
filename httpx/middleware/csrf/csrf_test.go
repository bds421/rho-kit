package csrf

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	secretpkg "github.com/bds421/rho-kit/core/v2/secret"
	securitycsrf "github.com/bds421/rho-kit/security/v2/csrf"
)

type secretFailingReader struct{}

func (secretFailingReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable: secret-token")
}

// --- Double-submit cookie CSRF (csrf.New) tests ---

func testSecret() []byte {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i + 1)
	}
	return secret
}

func generateSignedToken(s []byte) string {
	token, err := mintSignedToken(secretpkg.New(s))
	if err != nil {
		panic("csrf test: failed to generate signed token: " + err.Error())
	}
	return token
}

func TestWithSecretClonesInput(t *testing.T) {
	src := testSecret()
	opt := WithSecret(src)
	src[0] = 99

	var cfg config
	opt(&cfg)

	require.Equal(t, byte(1), cfg.secrets[0].Reveal()[0])
	// Wrapping in secret.String defends against caller-side mutation: the
	// original buffer is copied into the wrapper, so mutating the inner
	// buffer would require RevealString() to roundtrip — instead we assert
	// that two separate option applications hand back independent copies.

	var cfg2 config
	opt(&cfg2)
	require.Equal(t, byte(1), cfg2.secrets[0].Reveal()[0])
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

func TestNew_TokenMintFailureReturns500(t *testing.T) {
	prev := tokenRandReader
	tokenRandReader = secretFailingReader{}
	t.Cleanup(func() { tokenRandReader = prev })

	handler := New(WithSecret(testSecret()))(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Empty(t, rec.Header().Values("Set-Cookie"))
	assert.Contains(t, rec.Body.String(), "csrf: token mint failed")
	assert.NotContains(t, rec.Body.String(), "secret-token")
}

func TestNew_DevSecretGenerationPanicIsStable(t *testing.T) {
	prev := tokenRandReader
	tokenRandReader = secretFailingReader{}
	t.Cleanup(func() { tokenRandReader = prev })

	assert.PanicsWithValue(t, "csrf: failed to generate HMAC secret", func() {
		New(WithDevSecret())
	})
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

func TestNew_POSTRejectedWithDuplicateCSRFHeader(t *testing.T) {
	secret := testSecret()
	mw := New(WithSecret(secret))
	called := false
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	token := generateSignedToken(secret)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: token})
	req.Header.Add("X-CSRF-Token", token)
	req.Header.Add("X-CSRF-Token", token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, called)
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

func TestNew_CustomHeaderNameNotReflectedInError(t *testing.T) {
	secret := testSecret()
	mw := New(WithSecret(secret), WithHeaderName("X-Secret-CSRF-Header"))
	handler := mw(okHandler())

	token := generateSignedToken(secret)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: token})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "missing CSRF header")
	assert.NotContains(t, rec.Body.String(), "X-Secret-CSRF-Header")
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

func TestNew_WithSecretsAcceptsPreviousSecretDuringRotation(t *testing.T) {
	oldSecret := testSecret()
	newSecret := make([]byte, 32)
	for i := range newSecret {
		newSecret[i] = byte(i + 100)
	}

	mw := New(WithSecrets(newSecret, oldSecret))
	handler := mw(okHandler())

	token := generateSignedToken(oldSecret)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: token})
	req.Header.Set("X-CSRF-Token", token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.NotEqual(t, token, cookies[0].Value)
	assert.True(t, isValidSignedToken(cookies[0].Value, secretpkg.New(newSecret)))
}

func TestIsValidSignedToken_InvalidFormats(t *testing.T) {
	s := secretpkg.New(testSecret())
	assert.False(t, isValidSignedToken("", s))
	assert.False(t, isValidSignedToken("noseparator", s))
	assert.False(t, isValidSignedToken(".onlysuffix", s))
	assert.False(t, isValidSignedToken("onlyprefix.", s))
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

func TestNew_SkipCheckPanicEnforcesCSRF(t *testing.T) {
	called := false
	mw := New(WithSecret(testSecret()), WithSkipCheck(func(*http.Request) bool {
		panic("skip failed")
	}))
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	assert.NotPanics(t, func() {
		handler.ServeHTTP(rec, req)
	})

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, called)
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

func TestCSRF_OriginCheckBeforeSkipCheck(t *testing.T) {
	// M-9 fix: when both WithSkipCheck and WithAllowedOrigins are
	// configured, the Origin allowlist check MUST run BEFORE the skip
	// predicate. Otherwise a bearer-token POST from an unfamiliar origin
	// would slip through, defeating the allowlist for the very class of
	// caller most likely to be impersonated by a phished credential.
	mw := New(
		WithSecret(testSecret()),
		WithSkipCheck(HasBearerToken),
		WithAllowedOrigins("https://app.example.com"),
	)
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer phished-token")
	req.Header.Set("Origin", "https://attacker.example") // NOT in allowlist
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code,
		"Origin allowlist must reject before SkipCheck — phished bearer from attacker origin must NOT bypass CSRF")
}

func TestCSRF_OriginCheckBeforeSkipCheck_AllowedOriginPasses(t *testing.T) {
	// Sanity: a bearer-token POST from an allow-listed origin still
	// short-circuits via SkipCheck (origin passes, skip predicate
	// passes, no token required).
	mw := New(
		WithSecret(testSecret()),
		WithSkipCheck(HasBearerToken),
		WithAllowedOrigins("https://app.example.com"),
	)
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer good-token")
	req.Header.Set("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
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
		{"edge whitespace", " Bearer token123", false},
		{"token whitespace", "Bearer token123 ", false},
		{"internal token whitespace", "Bearer token 123", false},
		{"comma combined", "Bearer token123,other", false},
		{"control", "Bearer token123\n", false},
		{"invalid utf8", string([]byte{'B', 'e', 'a', 'r', 'e', 'r', ' ', 0xff}), false},
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

func TestHasBearerToken_RejectsDuplicateAuthorization(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Add("Authorization", "Bearer token123")
	req.Header.Add("Authorization", "Bearer token456")

	assert.False(t, HasBearerToken(req))
}

func TestHasBearerToken_NilRequest(t *testing.T) {
	assert.False(t, HasBearerToken(nil))
}

func TestHasAPIKey_NilRequest(t *testing.T) {
	assert.False(t, HasAPIKey("X-API-Key")(nil))
}

func TestNew_SkipCheck_DuplicateAuthorizationRequiresCSRF(t *testing.T) {
	mw := New(WithSecret(testSecret()), WithSkipCheck(HasBearerToken))
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Add("Authorization", "Bearer token123")
	req.Header.Add("Authorization", "Bearer token456")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestNew_SkipCheck_AmbiguousAuthorizationRequiresCSRF(t *testing.T) {
	mw := New(WithSecret(testSecret()), WithSkipCheck(HasBearerToken))
	handler := mw(okHandler())

	for name, value := range map[string]string{
		"edge whitespace":  " Bearer token123",
		"token whitespace": "Bearer token123 ",
		"internal space":   "Bearer token 123",
		"comma":            "Bearer token123,other",
		"control":          "Bearer token123\n",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.Header.Set("Authorization", value)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusForbidden, rec.Code)
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

func TestHasAPIKey_RejectsDuplicateHeader(t *testing.T) {
	predicate := HasAPIKey("X-API-Key")
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Add("X-API-Key", "secret")
	req.Header.Add("X-API-Key", "secret")

	assert.False(t, predicate(req))
}

func TestHasAPIKey_RejectsAmbiguousHeaderValues(t *testing.T) {
	predicate := HasAPIKey("X-API-Key")

	for name, value := range map[string]string{
		"edge whitespace":        " secret",
		"internal space":         "secret value",
		"comma combined":         "secret,other",
		"control":                "secret\n",
		"invalid utf8":           string([]byte{'s', 'e', 'c', 'r', 'e', 't', 0xff}),
		"horizontal tab":         "secret\tvalue",
		"carriage return":        "secret\rvalue",
		"unicode line separator": "secret\u2028value",
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.Header.Set("X-API-Key", value)

			assert.False(t, predicate(req))
		})
	}
}

func TestHasAPIKey_PanicsOnInvalidHeader(t *testing.T) {
	assert.Panics(t, func() { HasAPIKey("Bad Header") })
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

func TestRequireJSONContentType_POST_WithStructuredJSON(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"key":"value"}`))
	req.Header.Set("Content-Type", "application/merge-patch+json; charset=utf-8")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST with structured +json Content-Type should pass, got %d", rec.Code)
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

func TestRequireJSONContentType_POST_DuplicateContentType(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"key":"value"}`))
	req.Header.Add("Content-Type", "application/json")
	req.Header.Add("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("POST with duplicate Content-Type should be 415, got %d", rec.Code)
	}
}

func TestRequireJSONContentType_POST_InvalidContentTypeHeaderValue(t *testing.T) {
	handler := RequireJSONContentType(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for name, value := range map[string]string{
		"control":      "application/json\n",
		"invalid utf8": string([]byte{'a', 'p', 'p', 'l', 'i', 'c', 'a', 't', 'i', 'o', 'n', '/', 'j', 's', 'o', 'n', 0xff}),
	} {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"key":"value"}`))
			req.Header.Set("Content-Type", value)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusUnsupportedMediaType {
				t.Fatalf("POST with invalid Content-Type should be 415, got %d", rec.Code)
			}
		})
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

func TestWithAllowedOrigins_PanicsOnNoNonEmptyOrigins(t *testing.T) {
	assert.Panics(t, func() {
		WithAllowedOrigins("", " \t ")
	})
}

func TestWithAllowedOrigins_PanicsOnInvalidOrigins(t *testing.T) {
	for _, origin := range []string{
		"https://app.example.com/",
		"https://app.example.com/path",
		"https://app.example.com?x=1",
		"https://user@app.example.com",
		"https://app.example.com:bad",
		"https://app.example.com:+443",
		"ftp://app.example.com",
	} {
		t.Run(strings.ReplaceAll(origin, "/", "_"), func(t *testing.T) {
			assert.Panics(t, func() {
				WithAllowedOrigins(origin)
			})
		})
	}
}

func TestWithAllowedOrigins_InvalidOriginDoesNotEchoValue(t *testing.T) {
	assert.PanicsWithValue(t, "csrf: WithAllowedOrigins invalid origin", func() {
		WithAllowedOrigins("https://app.example.com/%zz?token=secret-token")
	})
}

func TestWithAllowedOriginsOptionReuseDoesNotShareMap(t *testing.T) {
	opt := WithAllowedOrigins("https://app.example.com")

	var cfg1 config
	opt(&cfg1)
	var cfg2 config
	opt(&cfg2)

	cfg1.allowedOrigins["https://mutated.example.com"] = struct{}{}
	_, ok := cfg2.allowedOrigins["https://mutated.example.com"]
	assert.False(t, ok)
}

func TestNew_AllowedOrigins_RejectsUnlistedOriginEvenWithAllowedReferer(t *testing.T) {
	secret := testSecret()
	mw := New(WithSecret(secret), WithAllowedOrigins("https://app.example.com"))
	handler := mw(okHandler())

	token := generateSignedToken(secret)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: token})
	req.Header.Set("X-CSRF-Token", token)
	req.Header.Set("Origin", "https://evil.attacker.com")
	req.Header.Set("Referer", "https://app.example.com/some/path")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestNew_AllowedOrigins_RejectsNullOriginEvenWithAllowedReferer(t *testing.T) {
	secret := testSecret()
	mw := New(WithSecret(secret), WithAllowedOrigins("https://app.example.com"))
	handler := mw(okHandler())

	token := generateSignedToken(secret)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: token})
	req.Header.Set("X-CSRF-Token", token)
	req.Header.Set("Origin", "null")
	req.Header.Set("Referer", "https://app.example.com/some/path")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestNew_AllowedOrigins_RejectsDuplicateOriginHeaders(t *testing.T) {
	secret := testSecret()
	mw := New(WithSecret(secret), WithAllowedOrigins("https://app.example.com"))
	handler := mw(okHandler())

	token := generateSignedToken(secret)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: token})
	req.Header.Set("X-CSRF-Token", token)
	req.Header.Add("Origin", "https://app.example.com")
	req.Header.Add("Origin", "https://app.example.com")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestNew_AllowedOrigins_RejectsMalformedRuntimeOrigin(t *testing.T) {
	secret := testSecret()
	mw := New(WithSecret(secret), WithAllowedOrigins("https://app.example.com"))
	handler := mw(okHandler())

	token := generateSignedToken(secret)
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.AddCookie(&http.Cookie{Name: "__csrf", Value: token})
	req.Header.Set("X-CSRF-Token", token)
	req.Header.Set("Origin", "https://app.example.com:+443")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
}

func TestNew_AllowedOrigins_RejectsInvalidRuntimeOriginHeaderValues(t *testing.T) {
	for name, tt := range map[string]struct {
		header string
		value  string
	}{
		"origin control":       {"Origin", "https://app.example.com\n"},
		"origin invalid utf8":  {"Origin", string([]byte("https://app.example.com\xff"))},
		"referer control":      {"Referer", "https://app.example.com/path\n"},
		"referer invalid utf8": {"Referer", string([]byte("https://app.example.com/path\xff"))},
	} {
		t.Run(name, func(t *testing.T) {
			secret := testSecret()
			mw := New(WithSecret(secret), WithAllowedOrigins("https://app.example.com"))
			called := false
			handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusOK)
			}))

			token := generateSignedToken(secret)
			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.AddCookie(&http.Cookie{Name: "__csrf", Value: token})
			req.Header.Set("X-CSRF-Token", token)
			req.Header.Set(tt.header, tt.value)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.False(t, called)
		})
	}
}

// --- Mandatory secret unconditionally ---

func TestNew_PanicsWithoutSecret(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when no secret is configured")
		}
		assert.Contains(t, r.(string), "no HMAC secret configured")
	}()
	_ = New() // no WithSecret, no WithDevSecret
}

func TestNew_DevSecretExplicitOptIn(t *testing.T) {
	mw := New(WithDevSecret()) // explicit per-process random fallback
	assert.NotNil(t, mw)
}

func TestNew_PanicsOnSameSiteNoneWithoutSecure(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for SameSite=None with explicit Secure=false")
		}
		assert.Contains(t, r.(string), "SameSite=None requires Secure=true")
	}()
	// FR-020: Secure now defaults to true. Have to explicitly opt out
	// to trigger the SameSite=None+!Secure panic.
	_ = New(WithSecret(testSecret()), WithSameSite(http.SameSiteNoneMode), WithSecure(false))
}

func TestNew_DefaultsSecureToTrue(t *testing.T) {
	// FR-020 [HIGH]: pre-fix the default was Secure=false, so the
	// production-safe default required users to know to call
	// WithSecure(true). Now Secure=true is the default; you have to
	// explicitly opt out for local plaintext development.
	mw := New(WithSecret(testSecret()))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	mw(okHandler()).ServeHTTP(rec, req)

	cookies := rec.Result().Cookies()
	require.NotEmpty(t, cookies, "CSRF middleware must set a cookie on first GET")
	assert.True(t, cookies[0].Secure, "CSRF cookie must be Secure by default (FR-020)")
}

func TestNew_SessionExtractorPanicRejects(t *testing.T) {
	called := false
	mw := New(WithSecret(testSecret()), WithSessionExtractor(func(*http.Request) string {
		panic("session failed")
	}))
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	assert.NotPanics(t, func() {
		handler.ServeHTTP(rec, req)
	})

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, called)
	assert.Empty(t, rec.Header().Values("Set-Cookie"))
}

func TestNew_WithSecretsReissuesPreviousSessionBoundToken(t *testing.T) {
	oldSecret := testSecret()
	newSecret := make([]byte, 32)
	for i := range newSecret {
		newSecret[i] = byte(i + 100)
	}
	const sessionID = "session-1"
	extractor := func(*http.Request) string { return sessionID }

	oldHandler := New(WithSecret(oldSecret), WithSessionExtractor(extractor))(okHandler())
	getReq := httptest.NewRequest(http.MethodGet, "/", nil)
	getRec := httptest.NewRecorder()
	oldHandler.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code)
	oldCookie := getRec.Result().Cookies()[0]

	rotatedHandler := New(WithSecrets(newSecret, oldSecret), WithSessionExtractor(extractor))(okHandler())
	postReq := httptest.NewRequest(http.MethodPost, "/", nil)
	postReq.AddCookie(oldCookie)
	postReq.Header.Set("X-CSRF-Token", oldCookie.Value)
	postRec := httptest.NewRecorder()
	rotatedHandler.ServeHTTP(postRec, postReq)

	require.Equal(t, http.StatusOK, postRec.Code)
	cookies := postRec.Result().Cookies()
	require.Len(t, cookies, 1)
	require.NotEqual(t, oldCookie.Value, cookies[0].Value)
	currentIssuer := securitycsrf.MustNewIssuer(newSecret)
	require.NoError(t, currentIssuer.Verify(securitycsrf.Token(cookies[0].Value), sessionID))
}

func TestNew_SameSiteNoneWithSecureIsAllowed(t *testing.T) {
	mw := New(WithSecret(testSecret()), WithSameSite(http.SameSiteNoneMode), WithSecure(true))
	assert.NotNil(t, mw)
}

func TestOptions_PanicOnInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
	}{
		{name: "cookie name", fn: func() { WithCookieName("bad cookie") }},
		{name: "header name", fn: func() { WithHeaderName("bad header") }},
		{name: "short secret", fn: func() { WithSecret([]byte("short")) }},
		{name: "same site", fn: func() { WithSameSite(http.SameSite(99)) }},
		{name: "path empty", fn: func() { WithPath("") }},
		{name: "path relative", fn: func() { WithPath("relative") }},
		{name: "session extractor", fn: func() { WithSessionExtractor(nil) }},
		{name: "session ttl zero", fn: func() { WithSessionTTL(0) }},
		{name: "session ttl negative", fn: func() { WithSessionTTL(-time.Second) }},
		{name: "skip check", fn: func() { WithSkipCheck(nil) }},
		{name: "new nil option", fn: func() { New(nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Panics(t, tt.fn)
		})
	}
}

func TestOptions_PanicsDoNotReflectInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		fn   func()
	}{
		{name: "cookie name", fn: func() { WithCookieName("bad cookie secret-token") }},
		{name: "header name", fn: func() { WithHeaderName("bad header secret-token") }},
		{name: "path", fn: func() { WithPath("secret-token") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				rec := recover()
				require.NotNil(t, rec)
				msg, ok := rec.(string)
				require.True(t, ok, "panic must be a stable string, got %T", rec)
				assert.NotContains(t, msg, "secret-token")
			}()
			tt.fn()
		})
	}
}

func TestWithSameSitePanicDoesNotReflectMode(t *testing.T) {
	assert.PanicsWithValue(t, "csrf: WithSameSite requires a valid SameSite mode", func() {
		WithSameSite(http.SameSite(99))
	})
}

func TestMintSignedToken_EntropyFailureReturnsError(t *testing.T) {
	prev := tokenRandReader
	tokenRandReader = secretFailingReader{}
	t.Cleanup(func() { tokenRandReader = prev })

	token, err := mintSignedToken(secretpkg.New(testSecret()))
	require.Error(t, err)
	assert.Empty(t, token)
	assert.Contains(t, err.Error(), "csrf: generate random token")
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
