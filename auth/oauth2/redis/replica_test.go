package redis_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	redisoauth "github.com/bds421/rho-kit/auth/oauth2/redis/v2"
	"github.com/bds421/rho-kit/auth/oauth2/v2"
)

// TestBrowserLoginSurvivesReplicaChangeAndRejectsCallbackReplay is the
// browser-auth durability proof. Replica A starts login, replica B receives
// the callback using the same Redis state store, and a restarted replica C
// reads the resulting session. The callback state is single-use, so replay
// fails closed even after a process change.
func TestBrowserLoginSurvivesReplicaChangeAndRejectsCallbackReplay(t *testing.T) {
	_, store := newStore(t)
	runBrowserReplicaContinuity(t, store)
}

func runBrowserReplicaContinuity(t *testing.T, store *redisoauth.Store) {
	t.Helper()
	issuer := newReplicaIssuer(t, "web")
	newClient := func() *oauth2.Client {
		client, err := oauth2.NewClient(context.Background(), oauth2.Config{
			Issuer: issuer.server.URL, ClientID: "web", RedirectURL: "https://app.example.test/oauth/callback",
		}, oauth2.WithSessionStore(store), oauth2.WithStateStore(store.States()), oauth2.WithInsecureCookie())
		require.NoError(t, err)
		return client
	}

	// Replica A begins the flow.
	first := newClient()
	login := httptest.NewRecorder()
	first.Handlers().ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/oauth/login", nil))
	require.Equal(t, http.StatusFound, login.Code)
	authURL, err := url.Parse(login.Result().Header.Get("Location"))
	require.NoError(t, err)
	state := authURL.Query().Get("state")
	require.NotEmpty(t, state)

	// Visiting the provider registers the one-time authorization code.
	providerResp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get(authURL.String())
	require.NoError(t, err)
	_ = providerResp.Body.Close()

	// Replica B owns the callback after A has disappeared.
	second := newClient()
	callback := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=code-"+state+"&state="+state, nil)
	for _, cookie := range login.Result().Cookies() {
		callback.AddCookie(cookie)
	}
	callbackResp := httptest.NewRecorder()
	second.Handlers().ServeHTTP(callbackResp, callback)
	require.Equal(t, http.StatusOK, callbackResp.Code, callbackResp.Body.String())

	var sessionCookie *http.Cookie
	for _, cookie := range callbackResp.Result().Cookies() {
		if cookie.Name == "kit_oauth_session" {
			sessionCookie = cookie
		}
	}
	require.NotNil(t, sessionCookie)

	// Replica C simulates process restart and reads the Redis session.
	third := newClient()
	request := httptest.NewRequest(http.MethodGet, "/orders", nil)
	request.AddCookie(sessionCookie)
	session, err := third.SessionFromRequest(request.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, "user-123", session.UserID)

	replay := httptest.NewRecorder()
	third.Handlers().ServeHTTP(replay, callback)
	assert.Equal(t, http.StatusBadRequest, replay.Code, "callback state must be single-use across replicas")
}

type replicaIssuer struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	client string
	mu     sync.Mutex
	codes  map[string]string
}

func newReplicaIssuer(t *testing.T, client string) *replicaIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	f := &replicaIssuer{server: server, key: key, client: client, codes: map[string]string{}}
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"issuer": server.URL, "authorization_endpoint": server.URL + "/authorize", "token_endpoint": server.URL + "/token", "jwks_uri": server.URL + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "replica-key", Algorithm: "RS256", Use: "sig"}}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code := "code-" + q.Get("state")
		f.mu.Lock()
		f.codes[code] = q.Get("nonce")
		f.mu.Unlock()
		redirect, _ := url.Parse(q.Get("redirect_uri"))
		values := redirect.Query()
		values.Set("code", code)
		values.Set("state", q.Get("state"))
		redirect.RawQuery = values.Encode()
		http.Redirect(w, r, redirect.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		nonce, ok := f.codes[r.Form.Get("code")]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "unknown code", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "opaque", "token_type": "Bearer", "expires_in": 3600, "id_token": f.idToken(t, nonce)})
	})
	return f
}

func (f *replicaIssuer) idToken(t *testing.T, nonce string) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: f.key}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "replica-key"))
	require.NoError(t, err)
	raw, err := jwt.Signed(signer).Claims(map[string]any{"iss": f.server.URL, "sub": "user-123", "aud": f.client, "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": nonce}).Serialize()
	require.NoError(t, err)
	return raw
}
