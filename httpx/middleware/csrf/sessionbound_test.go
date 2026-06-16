package csrf

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	secretpkg "github.com/bds421/rho-kit/core/v2/secret"
	securitycsrf "github.com/bds421/rho-kit/security/v2/csrf"
)

// session-bound CSRF: when WithSessionExtractor is configured, the
// middleware delegates issue/verify to the security/csrf primitive so a
// token minted for session A cannot be replayed against session B.

func sessionFromHeader(headerName string) func(*http.Request) string {
	return func(r *http.Request) string { return r.Header.Get(headerName) }
}

func TestSessionBound_IssuesTokenAndAccepts(t *testing.T) {
	mw := New(
		WithSecret(testSecret()),
		WithSessionExtractor(sessionFromHeader("X-Session")),
	)
	handler := mw(okHandler())

	// Initial GET seeds the cookie for session "alice".
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Session", "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	token := cookies[0].Value

	// State-changing request with the cookie + matching header for
	// session "alice" succeeds.
	post := httptest.NewRequest(http.MethodPost, "/", nil)
	post.Header.Set("X-Session", "alice")
	post.AddCookie(cookies[0])
	post.Header.Set(defaultHeaderName, token)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, post)
	assert.Equal(t, http.StatusOK, rec2.Code)
}

func TestSessionBound_RejectsCrossSessionReplay(t *testing.T) {
	// The whole point of session binding: a token minted for "alice"
	// must be rejected when presented under "bob"'s session.
	mw := New(
		WithSecret(testSecret()),
		WithSessionExtractor(sessionFromHeader("X-Session")),
	)
	handler := mw(okHandler())

	// Mint as alice.
	get := httptest.NewRequest(http.MethodGet, "/", nil)
	get.Header.Set("X-Session", "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, get)
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	token := cookies[0].Value

	// Replay as bob.
	post := httptest.NewRequest(http.MethodPost, "/", nil)
	post.Header.Set("X-Session", "bob")
	// Must inject the cookie + header under bob's session.
	post.AddCookie(cookies[0])
	post.Header.Set(defaultHeaderName, token)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, post)
	assert.Equal(t, http.StatusForbidden, rec2.Code)
}

func TestSessionBound_RejectsDuplicateCSRFHeader(t *testing.T) {
	mw := New(
		WithSecret(testSecret()),
		WithSessionExtractor(sessionFromHeader("X-Session")),
	)
	called := false
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Session", "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	token := cookies[0].Value
	called = false

	post := httptest.NewRequest(http.MethodPost, "/", nil)
	post.Header.Set("X-Session", "alice")
	post.AddCookie(cookies[0])
	post.Header.Add(defaultHeaderName, token)
	post.Header.Add(defaultHeaderName, token)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, post)

	assert.Equal(t, http.StatusForbidden, rec2.Code)
	assert.False(t, called)
}

func TestSessionBound_RejectsTamperedToken(t *testing.T) {
	mw := New(
		WithSecret(testSecret()),
		WithSessionExtractor(sessionFromHeader("X-Session")),
	)
	handler := mw(okHandler())

	get := httptest.NewRequest(http.MethodGet, "/", nil)
	get.Header.Set("X-Session", "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, get)
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	tampered := cookies[0].Value + "x"

	post := httptest.NewRequest(http.MethodPost, "/", nil)
	post.Header.Set("X-Session", "alice")
	tamperedCookie := *cookies[0]
	tamperedCookie.Value = tampered
	post.AddCookie(&tamperedCookie)
	post.Header.Set(defaultHeaderName, tampered)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, post)
	assert.Equal(t, http.StatusForbidden, rec2.Code)
}

func TestSessionBound_GETPassesWithoutHeader(t *testing.T) {
	// GET seeds a cookie even with session binding; safe-method exemption
	// from token check is preserved.
	mw := New(
		WithSecret(testSecret()),
		WithSessionExtractor(sessionFromHeader("X-Session")),
	)
	handler := mw(okHandler())

	get := httptest.NewRequest(http.MethodGet, "/", nil)
	get.Header.Set("X-Session", "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, get)
	assert.Equal(t, http.StatusOK, rec.Code)
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.NotEmpty(t, cookies[0].Value)
}

func TestSessionBound_MissingSessionRejected(t *testing.T) {
	// If the extractor returns "", we'd be back to per-process pinning
	// only — defeating the purpose of session binding. Reject explicitly.
	mw := New(
		WithSecret(testSecret()),
		WithSessionExtractor(sessionFromHeader("X-Session")),
	)
	handler := mw(okHandler())

	post := httptest.NewRequest(http.MethodPost, "/", nil)
	// No X-Session header set.
	post.Header.Set(defaultHeaderName, "garbage")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, post)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, rec.Header().Values("Set-Cookie"))
}

func TestSessionBound_InvalidSessionRejectedBeforeIssuingCookie(t *testing.T) {
	mw := New(
		WithSecret(testSecret()),
		WithSessionExtractor(sessionFromHeader("X-Session")),
	)
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-Session", "alice\nadmin")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Empty(t, rec.Header().Values("Set-Cookie"))
}

func TestSessionBound_AnonymousSafeMethodPasses(t *testing.T) {
	// WithSessionExtractor docs scope the session requirement to
	// "every authenticated state-changing request". An anonymous user
	// (extractor returns "") doing a plain page load must not be 403'd
	// — that would block every anonymous GET when the middleware is
	// mounted globally. No cookie can be issued without a session to
	// bind it to, so none is set.
	mw := New(
		WithSecret(testSecret()),
		WithSessionExtractor(sessionFromHeader("X-Session")),
	)
	handler := mw(okHandler())

	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/", nil)
			// No X-Session header: anonymous request.
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Empty(t, rec.Header().Values("Set-Cookie"),
				"no session means no token to bind, so no cookie is set")
		})
	}
}

func TestSessionBound_AnonymousStateChangingStillRejected(t *testing.T) {
	// The state-changing half of the contract is preserved: an empty
	// session on a mutating request still 403s rather than falling back
	// to per-process pinning. No cookie is issued.
	mw := New(
		WithSecret(testSecret()),
		WithSessionExtractor(sessionFromHeader("X-Session")),
	)
	handler := mw(okHandler())

	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/", nil)
			// No X-Session header: anonymous request.
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusForbidden, rec.Code)
			assert.Empty(t, rec.Header().Values("Set-Cookie"))
		})
	}
}

func TestSessionBound_SkipCheckBypassesSessionGate(t *testing.T) {
	// WithSkipCheck documents bearer/API-key clients as the use case:
	// "If skip returns true for a request, CSRF token validation is
	// skipped." A header-authenticated client typically has no browser
	// session, so the session-required gate must not silently 403 it
	// before the skip predicate is consulted.
	mw := New(
		WithSecret(testSecret()),
		WithSessionExtractor(sessionFromHeader("X-Session")),
		WithSkipCheck(HasBearerToken),
	)
	handler := mw(okHandler())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	// No X-Session header (header-auth client, no browser session).
	req.Header.Set("Authorization", "Bearer some-jwt-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code,
		"bearer-authenticated request must bypass the session-required gate via WithSkipCheck")
}

func TestSessionBound_SkipCheckStillSubjectToOriginAllowlist(t *testing.T) {
	// Defense-in-depth: even when the skip predicate would bypass the
	// session gate, an Origin outside the allowlist must still be
	// rejected — matching the double-submit flow's M-9 ordering.
	mw := New(
		WithSecret(testSecret()),
		WithSessionExtractor(sessionFromHeader("X-Session")),
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
		"untrusted origin must reject before the skip predicate even with an empty session")
}

// mutableClock is a test clock whose Now value can be advanced.
type mutableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *mutableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mutableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// TestSessionBound_TTLExpiry_ReissuesAndRejects drives a token past its TTL via
// a fake clock injected into the issuer (matching the WithKeyedClock-style
// fixtures used elsewhere). An expired token must no longer verify, so a
// state-changing request triggers the reissue path and the deterministic
// "CSRF cookie was reissued; retry" 403 (csrf.go reissue + cookieRegenerated).
func TestSessionBound_TTLExpiry_ReissuesAndRejects(t *testing.T) {
	const ttl = time.Hour
	clk := &mutableClock{now: time.Unix(1_700_000_000, 0)}

	// Build the session-bound middleware directly so we can inject the fake
	// clock through the issuer (the public New does not expose a clock).
	cfg := config{
		cookieName:       defaultCookieName,
		headerName:       defaultHeaderName,
		path:             "/",
		secure:           true,
		sameSite:         http.SameSiteStrictMode,
		secrets:          []*secretpkg.String{secretpkg.New(testSecret())},
		sessionExtractor: sessionFromHeader("X-Session"),
		sessionTTL:       ttl,
	}
	issuerOpts := []securitycsrf.Option{
		securitycsrf.WithTTL(ttl),
		securitycsrf.WithClock(clk.Now),
	}
	issuer := securitycsrf.MustNewIssuer(testSecret(), issuerOpts...)
	currentIssuer := securitycsrf.MustNewIssuer(testSecret(), issuerOpts...)
	handler := sessionBoundMiddleware(&cfg, issuer, currentIssuer)(okHandler())

	// Seed a cookie for alice at t0.
	get := httptest.NewRequest(http.MethodGet, "/", nil)
	get.Header.Set("X-Session", "alice")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, get)
	require.Equal(t, http.StatusOK, rec.Code)
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	token := cookies[0].Value

	// Within TTL the token verifies and the POST succeeds.
	post := httptest.NewRequest(http.MethodPost, "/", nil)
	post.Header.Set("X-Session", "alice")
	post.AddCookie(cookies[0])
	post.Header.Set(defaultHeaderName, token)
	recOK := httptest.NewRecorder()
	handler.ServeHTTP(recOK, post)
	require.Equal(t, http.StatusOK, recOK.Code, "token must verify within TTL")

	// Advance past the TTL: the stored token no longer verifies, so the
	// request hits the reissue path and the retry-required 403.
	clk.advance(ttl + time.Second)
	expired := httptest.NewRequest(http.MethodPost, "/", nil)
	expired.Header.Set("X-Session", "alice")
	expired.AddCookie(cookies[0])
	expired.Header.Set(defaultHeaderName, token)
	recExpired := httptest.NewRecorder()
	handler.ServeHTTP(recExpired, expired)

	assert.Equal(t, http.StatusForbidden, recExpired.Code,
		"expired token must be rejected via the reissue+403 path")
	// A fresh cookie must be issued so the client can retry.
	require.NotEmpty(t, recExpired.Result().Cookies(),
		"expiry must reissue a new cookie for retry")
	assert.NotEqual(t, token, recExpired.Result().Cookies()[0].Value,
		"reissued token must differ from the expired one")
}
