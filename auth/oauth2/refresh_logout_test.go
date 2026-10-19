package oauth2_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/bds421/rho-kit/core/v2/secret"
)

func (f *fakeIssuer) signClaims(t *testing.T, subject string, extra map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: f.signingKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key-1"),
	)
	require.NoError(t, err)
	claims := map[string]any{
		"iss": f.server.URL,
		"sub": subject,
		"aud": f.clientID,
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	for k, v := range extra {
		claims[k] = v
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return raw
}

type refreshFixture struct {
	issuer   *fakeIssuer
	sessions *oauth2.MemorySessionStore
	client   *oauth2.Client
	calls    atomic.Int64
}

// newRefreshFixture serves refresh grants with respond and seeds session "s1"
// whose access token expires at expiry.
func newRefreshFixture(t *testing.T, expiry time.Time, respond func(w http.ResponseWriter, r *http.Request), opts ...oauth2.Option) *refreshFixture {
	t.Helper()
	f := &refreshFixture{issuer: newFakeIssuer(t, "test-client"), sessions: oauth2.NewMemorySessionStore()}
	f.issuer.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		require.NoError(t, r.ParseForm())
		assert.Equal(t, "refresh_token", r.FormValue("grant_type"))
		assert.Equal(t, "refresh-1", r.FormValue("refresh_token"))
		respond(w, r)
	}
	opts = append([]oauth2.Option{
		oauth2.WithSessionStore(f.sessions),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithInsecureCookie(),
	}, opts...)
	client, err := oauth2.NewClient(context.Background(), oauth2.Config{
		Issuer: f.issuer.server.URL, ClientID: "test-client", RedirectURL: "https://app/cb",
	}, opts...)
	require.NoError(t, err)
	f.client = client
	require.NoError(t, f.sessions.Put(context.Background(), "s1", oauth2.Session{
		SessionID:    "s1",
		UserID:       "user-123",
		AccessToken:  secret.NewFromString("access-1"),
		RefreshToken: secret.NewFromString("refresh-1"),
		Expiry:       expiry,
		Claims:       map[string]any{"email": "old@example.com"},
	}, time.Hour))
	return f
}

func (f *refreshFixture) session(t *testing.T) (oauth2.Session, error) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "kit_oauth_session", Value: "s1"})
	return f.client.SessionFromRequest(context.Background(), req)
}

func writeTokens(t *testing.T, w http.ResponseWriter, body map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	require.NoError(t, json.NewEncoder(w).Encode(body))
}

func TestTokenRefresh_SingleFlightAndPersist(t *testing.T) {
	var f *refreshFixture
	release := make(chan struct{})
	f = newRefreshFixture(t, time.Now().Add(-time.Minute), func(w http.ResponseWriter, _ *http.Request) {
		<-release
		writeTokens(t, w, map[string]any{
			"access_token": "access-2", "token_type": "Bearer", "refresh_token": "refresh-2", "expires_in": 3600,
			"id_token": f.issuer.signClaims(t, "user-123", map[string]any{"email": "new@example.com"}),
		})
	}, oauth2.WithTokenRefresh(time.Minute))

	var wg sync.WaitGroup
	results := make(chan oauth2.Session, 8)
	for range 8 {
		wg.Go(func() {
			sess, err := f.session(t)
			assert.NoError(t, err)
			results <- sess
		})
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	close(results)
	for sess := range results {
		assert.Equal(t, "access-2", sess.AccessToken.RevealString())
	}
	assert.Equal(t, int64(1), f.calls.Load(), "concurrent requests must share one refresh")

	stored, err := f.sessions.Get(context.Background(), "s1")
	require.NoError(t, err)
	assert.Equal(t, "access-2", stored.AccessToken.RevealString())
	assert.Equal(t, "refresh-2", stored.RefreshToken.RevealString())
	assert.Equal(t, "new@example.com", stored.Claims["email"])
	assert.NotNil(t, stored.IDToken)
	assert.True(t, stored.Expiry.After(time.Now().Add(30*time.Minute)))

	// Fresh now: no further token-endpoint call.
	_, err = f.session(t)
	require.NoError(t, err)
	assert.Equal(t, int64(1), f.calls.Load())
}

func TestTokenRefresh_KeepsRefreshTokenWhenNotRotated(t *testing.T) {
	f := newRefreshFixture(t, time.Now().Add(30*time.Second), func(w http.ResponseWriter, _ *http.Request) {
		writeTokens(t, w, map[string]any{"access_token": "access-2", "token_type": "Bearer", "expires_in": 3600})
	}, oauth2.WithTokenRefresh(time.Minute))
	sess, err := f.session(t)
	require.NoError(t, err)
	assert.Equal(t, "access-2", sess.AccessToken.RevealString())
	assert.Equal(t, "refresh-1", sess.RefreshToken.RevealString())
	assert.Equal(t, "old@example.com", sess.Claims["email"], "claims stay when no id_token is returned")
}

func TestTokenRefresh_DisabledByDefaultAndSkipsFreshTokens(t *testing.T) {
	never := func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "unexpected", http.StatusTeapot) }
	f := newRefreshFixture(t, time.Now().Add(-time.Minute), never)
	sess, err := f.session(t)
	require.NoError(t, err)
	assert.Equal(t, "access-1", sess.AccessToken.RevealString())

	f = newRefreshFixture(t, time.Now().Add(time.Hour), never, oauth2.WithTokenRefresh(time.Minute))
	sess, err = f.session(t)
	require.NoError(t, err)
	assert.Equal(t, "access-1", sess.AccessToken.RevealString())
	assert.Zero(t, f.calls.Load())
}

func TestTokenRefresh_RejectedRefreshExpiresSession(t *testing.T) {
	f := newRefreshFixture(t, time.Now().Add(30*time.Second), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}, oauth2.WithTokenRefresh(time.Minute))
	_, err := f.session(t)
	require.ErrorIs(t, err, oauth2.ErrSessionNotFound)
	_, err = f.sessions.Get(context.Background(), "s1")
	assert.ErrorIs(t, err, oauth2.ErrSessionNotFound, "rejected refresh must delete the session")
}

func TestTokenRefresh_TransientFailure(t *testing.T) {
	unavailable := func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusServiceUnavailable) }

	f := newRefreshFixture(t, time.Now().Add(30*time.Second), unavailable, oauth2.WithTokenRefresh(time.Minute))
	sess, err := f.session(t)
	require.NoError(t, err, "unexpired session survives a transient refresh failure")
	assert.Equal(t, "access-1", sess.AccessToken.RevealString())

	f = newRefreshFixture(t, time.Now().Add(-time.Second), unavailable, oauth2.WithTokenRefresh(time.Minute))
	_, err = f.session(t)
	require.ErrorIs(t, err, oauth2.ErrSessionNotFound, "expired session cannot be kept on failure")
}

func TestTokenRefresh_SubjectChangeExpiresSession(t *testing.T) {
	var f *refreshFixture
	f = newRefreshFixture(t, time.Now().Add(-time.Minute), func(w http.ResponseWriter, _ *http.Request) {
		writeTokens(t, w, map[string]any{
			"access_token": "access-2", "token_type": "Bearer", "expires_in": 3600,
			"id_token": f.issuer.signClaims(t, "someone-else", nil),
		})
	}, oauth2.WithTokenRefresh(time.Minute))
	_, err := f.session(t)
	require.ErrorIs(t, err, oauth2.ErrSessionNotFound)
}

func TestWithTokenRefresh_PanicsOnNegativeLeeway(t *testing.T) {
	assert.Panics(t, func() { oauth2.WithTokenRefresh(-time.Second) })
}

func TestRPInitiatedLogout_RequiresEndSessionEndpoint(t *testing.T) {
	issuer := newFakeIssuer(t, "test-client")
	_, err := oauth2.NewClient(context.Background(), oauth2.Config{
		Issuer: issuer.server.URL, ClientID: "test-client", RedirectURL: "https://app/cb",
	}, oauth2.WithSessionStore(oauth2.NewMemorySessionStore()), oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithRPInitiatedLogout(""))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "end_session_endpoint")
}

func TestRPInitiatedLogout_PanicsOnInvalidRedirect(t *testing.T) {
	for _, uri := range []string{"/relative", "javascript:alert(1)", "https://user@host/x", "::"} {
		assert.Panics(t, func() { oauth2.WithRPInitiatedLogout(uri) }, uri)
	}
}

func TestRPInitiatedLogout_ReturnsProviderLogoutURL(t *testing.T) {
	issuer := newFakeIssuer(t, "test-client")
	issuer.discoveryExtra = map[string]any{"end_session_endpoint": issuer.server.URL + "/logout?ui=1"}
	sessions := oauth2.NewMemorySessionStore()
	client, err := oauth2.NewClient(context.Background(), oauth2.Config{
		Issuer: issuer.server.URL, ClientID: "test-client", RedirectURL: "https://app/cb",
	}, oauth2.WithSessionStore(sessions), oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithInsecureCookie(), oauth2.WithRPInitiatedLogout("https://app.example.test/signed-out"))
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle("/oauth/", client.Handlers())

	state, cookies := runLoginAndAuthorize(t, mux, "/oauth/login")
	cbReq := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=code-"+state+"&state="+state, nil)
	addCookies(cbReq, cookies)
	cbRes := httptest.NewRecorder()
	mux.ServeHTTP(cbRes, cbReq)
	require.Equal(t, http.StatusOK, cbRes.Code, cbRes.Body.String())
	var sessionCookie *http.Cookie
	for _, ck := range cbRes.Result().Cookies() {
		if ck.Name == "kit_oauth_session" {
			sessionCookie = ck
		}
	}
	require.NotNil(t, sessionCookie)
	stored, err := sessions.Get(context.Background(), sessionCookie.Value)
	require.NoError(t, err)
	require.NotNil(t, stored.IDToken, "callback must record the raw ID token")

	logoutReq := httptest.NewRequest(http.MethodPost, "/oauth/logout", nil)
	logoutReq.AddCookie(sessionCookie)
	logoutRes := httptest.NewRecorder()
	mux.ServeHTTP(logoutRes, logoutReq)
	require.Equal(t, http.StatusOK, logoutRes.Code)
	assert.Equal(t, "no-store", logoutRes.Header().Get("Cache-Control"))
	var body oauth2.LogoutResponse
	require.NoError(t, json.NewDecoder(logoutRes.Body).Decode(&body))
	u, err := url.Parse(body.LogoutURL)
	require.NoError(t, err)
	assert.Equal(t, issuer.server.URL+"/logout", u.Scheme+"://"+u.Host+u.Path)
	q := u.Query()
	assert.Equal(t, "1", q.Get("ui"), "existing endpoint query is preserved")
	assert.Equal(t, "test-client", q.Get("client_id"))
	assert.Equal(t, stored.IDToken.RevealString(), q.Get("id_token_hint"))
	assert.Equal(t, "https://app.example.test/signed-out", q.Get("post_logout_redirect_uri"))
	_, err = sessions.Get(context.Background(), sessionCookie.Value)
	assert.ErrorIs(t, err, oauth2.ErrSessionNotFound)

	// Without a session the URL is still returned, just without a hint.
	anonRes := httptest.NewRecorder()
	mux.ServeHTTP(anonRes, httptest.NewRequest(http.MethodPost, "/oauth/logout", nil))
	require.Equal(t, http.StatusOK, anonRes.Code)
	require.NoError(t, json.NewDecoder(anonRes.Body).Decode(&body))
	u, err = url.Parse(body.LogoutURL)
	require.NoError(t, err)
	assert.Empty(t, u.Query().Get("id_token_hint"))
}

func TestLogout_DefaultStays204(t *testing.T) {
	issuer := newFakeIssuer(t, "test-client")
	issuer.discoveryExtra = map[string]any{"end_session_endpoint": issuer.server.URL + "/logout"}
	client, err := oauth2.NewClient(context.Background(), oauth2.Config{
		Issuer: issuer.server.URL, ClientID: "test-client", RedirectURL: "https://app/cb",
	}, oauth2.WithSessionStore(oauth2.NewMemorySessionStore()), oauth2.WithStateStore(oauth2.NewMemoryStateStore()))
	require.NoError(t, err)
	res := httptest.NewRecorder()
	client.Handlers().ServeHTTP(res, httptest.NewRequest(http.MethodPost, "/oauth/logout", nil))
	assert.Equal(t, http.StatusNoContent, res.Code)
	assert.Empty(t, res.Body.String())
}

