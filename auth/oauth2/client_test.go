package oauth2_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
)

// fakeIssuer is a minimal OIDC provider that serves a real JWKS +
// signed id_tokens so go-oidc's verifier accepts them. Single RSA key,
// kid "test-key-1".
type fakeIssuer struct {
	server     *httptest.Server
	mux        *http.ServeMux
	signingKey *rsa.PrivateKey
	clientID   string

	mu    sync.Mutex
	codes map[string]codeRecord
}

type codeRecord struct {
	nonce         string
	codeChallenge string
}

func newFakeIssuer(t *testing.T, clientID string) *fakeIssuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f := &fakeIssuer{
		server:     srv,
		mux:        mux,
		signingKey: key,
		clientID:   clientID,
		codes:      make(map[string]codeRecord),
	}

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                srv.URL,
			"authorization_endpoint":                srv.URL + "/authorize",
			"token_endpoint":                        srv.URL + "/token",
			"jwks_uri":                              srv.URL + "/jwks",
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		jwks := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key:       &key.PublicKey,
			KeyID:     "test-key-1",
			Algorithm: "RS256",
			Use:       "sig",
		}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(jwks)
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		code := "code-" + q.Get("state")
		f.mu.Lock()
		f.codes[code] = codeRecord{nonce: q.Get("nonce"), codeChallenge: q.Get("code_challenge")}
		f.mu.Unlock()
		redirectURI := q.Get("redirect_uri")
		u, _ := url.Parse(redirectURI)
		rq := u.Query()
		rq.Set("code", code)
		rq.Set("state", q.Get("state"))
		u.RawQuery = rq.Encode()
		http.Redirect(w, r, u.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code := r.FormValue("code")
		f.mu.Lock()
		rec, ok := f.codes[code]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "unknown code", http.StatusBadRequest)
			return
		}
		idToken := f.signIDToken(t, rec.nonce)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "fake-access",
			"token_type":    "Bearer",
			"refresh_token": "fake-refresh",
			"expires_in":    3600,
			"id_token":      idToken,
		})
	})
	return f
}

// signIDToken builds a real RS256-signed ID token go-oidc's verifier
// accepts. iss matches the discovered issuer; aud matches the client ID.
func (f *fakeIssuer) signIDToken(t *testing.T, nonce string) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: f.signingKey},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key-1"),
	)
	require.NoError(t, err)
	claims := map[string]any{
		"iss":   f.server.URL,
		"sub":   "user-123",
		"aud":   f.clientID,
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
		"nonce": nonce,
		"email": "user@example.com",
	}
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	require.NoError(t, err)
	return raw
}

func TestNewClient_HappyPath(t *testing.T) {
	issuer := newFakeIssuer(t, "test-client")
	client, err := oauth2.NewClient(context.Background(),
		oauth2.Config{
			Issuer:      issuer.server.URL,
			ClientID:    "test-client",
			RedirectURL: "https://app.example.com/oauth/callback",
			Scopes:      []string{"profile", "email"},
		},
		oauth2.WithSessionStore(oauth2.NewMemorySessionStore()),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithInsecureCookie(),
	)
	require.NoError(t, err)
	require.NotNil(t, client)
	require.NotNil(t, client.OAuth2Config())
	require.NotNil(t, client.Provider())
}

func TestNewClient_MissingFields(t *testing.T) {
	cases := []oauth2.Config{
		{ClientID: "c", RedirectURL: "u"},
		{Issuer: "i", RedirectURL: "u"},
		{Issuer: "i", ClientID: "c"},
	}
	for i, c := range cases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			_, err := oauth2.NewClient(context.Background(), c)
			require.Error(t, err)
		})
	}
}

func TestNewClient_RequiresStores(t *testing.T) {
	issuer := newFakeIssuer(t, "c")
	_, err := oauth2.NewClient(context.Background(),
		oauth2.Config{Issuer: issuer.server.URL, ClientID: "c", RedirectURL: "https://app/cb"},
	)
	require.Error(t, err)
}

func TestNewClient_IssuerMismatchRejected(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 "https://different-issuer.example",
			"authorization_endpoint": "https://x/auth",
			"token_endpoint":         "https://x/token",
			"jwks_uri":               "https://x/jwks",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	_, err := oauth2.NewClient(context.Background(),
		oauth2.Config{Issuer: srv.URL, ClientID: "c", RedirectURL: "https://app/cb"},
		oauth2.WithSessionStore(oauth2.NewMemorySessionStore()),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
	)
	require.ErrorIs(t, err, oauth2.ErrIssuerDiscovery)
}

func TestHandlers_LoginRedirectsWithStateAndPKCE(t *testing.T) {
	issuer := newFakeIssuer(t, "test-client")
	client, _ := oauth2.NewClient(context.Background(),
		oauth2.Config{
			Issuer: issuer.server.URL, ClientID: "test-client",
			RedirectURL: "https://app/cb", Scopes: []string{"profile"},
		},
		oauth2.WithSessionStore(oauth2.NewMemorySessionStore()),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithInsecureCookie(),
	)
	mux := http.NewServeMux()
	mux.Handle("/oauth/", client.Handlers())
	srv := httptest.NewServer(mux)
	defer srv.Close()

	httpc := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := httpc.Get(srv.URL + "/oauth/login")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusFound, resp.StatusCode)
	loc, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	q := loc.Query()
	require.Equal(t, "code", q.Get("response_type"))
	require.Equal(t, "test-client", q.Get("client_id"))
	require.NotEmpty(t, q.Get("state"))
	require.NotEmpty(t, q.Get("nonce"))
	require.NotEmpty(t, q.Get("code_challenge"))
	require.Equal(t, "S256", q.Get("code_challenge_method"))
}

func TestHandlers_EndToEndCallbackSetsSession(t *testing.T) {
	issuer := newFakeIssuer(t, "test-client")
	sessionStore := oauth2.NewMemorySessionStore()
	stateStore := oauth2.NewMemoryStateStore()
	client, _ := oauth2.NewClient(context.Background(),
		oauth2.Config{
			Issuer: issuer.server.URL, ClientID: "test-client",
			RedirectURL: "https://app/cb", Scopes: []string{"profile"},
		},
		oauth2.WithSessionStore(sessionStore),
		oauth2.WithStateStore(stateStore),
		oauth2.WithInsecureCookie(),
	)
	mux := http.NewServeMux()
	mux.Handle("/oauth/", client.Handlers())
	appSrv := httptest.NewServer(mux)
	defer appSrv.Close()

	httpc := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	loginResp, err := httpc.Get(appSrv.URL + "/oauth/login")
	require.NoError(t, err)
	defer loginResp.Body.Close()
	authURL, err := url.Parse(loginResp.Header.Get("Location"))
	require.NoError(t, err)
	state := authURL.Query().Get("state")

	// Visit /authorize so the fake registers the code.
	authResp, err := httpc.Get(authURL.String())
	require.NoError(t, err)
	defer authResp.Body.Close()
	require.Equal(t, http.StatusFound, authResp.StatusCode)

	cbReq := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=code-"+state+"&state="+state, nil)
	cbResp := httptest.NewRecorder()
	mux.ServeHTTP(cbResp, cbReq)
	require.Equal(t, http.StatusNoContent, cbResp.Code, "callback: %s", cbResp.Body.String())

	cookies := cbResp.Result().Cookies()
	require.NotEmpty(t, cookies)
	var sessionID string
	for _, ck := range cookies {
		if ck.Name == "kit_oauth_session" {
			sessionID = ck.Value
		}
	}
	require.NotEmpty(t, sessionID)

	sess, err := sessionStore.Get(context.Background(), sessionID)
	require.NoError(t, err)
	require.Equal(t, "user-123", sess.UserID)
	require.Equal(t, "fake-access", sess.AccessToken.RevealString())
	require.Equal(t, "user@example.com", sess.Claims["email"])
}

func TestHandlers_CallbackRejectsUnknownState(t *testing.T) {
	issuer := newFakeIssuer(t, "test-client")
	client, _ := oauth2.NewClient(context.Background(),
		oauth2.Config{
			Issuer: issuer.server.URL, ClientID: "test-client",
			RedirectURL: "https://app/cb",
		},
		oauth2.WithSessionStore(oauth2.NewMemorySessionStore()),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithInsecureCookie(),
	)
	mux := http.NewServeMux()
	mux.Handle("/oauth/", client.Handlers())

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=c&state=unknown", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.True(t, strings.Contains(rec.Body.String(), "state mismatch"), "body: %s", rec.Body.String())
}

func TestHandlers_LogoutClearsCookie(t *testing.T) {
	issuer := newFakeIssuer(t, "test-client")
	client, _ := oauth2.NewClient(context.Background(),
		oauth2.Config{
			Issuer: issuer.server.URL, ClientID: "test-client",
			RedirectURL: "https://app/cb",
		},
		oauth2.WithSessionStore(oauth2.NewMemorySessionStore()),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithInsecureCookie(),
	)
	mux := http.NewServeMux()
	mux.Handle("/oauth/", client.Handlers())

	req := httptest.NewRequest(http.MethodPost, "/oauth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "kit_oauth_session", Value: "any"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
	cookies := rec.Result().Cookies()
	require.NotEmpty(t, cookies)
	require.Less(t, cookies[0].MaxAge, 0)
}

func TestMemorySessionStore_Expiry(t *testing.T) {
	s := oauth2.NewMemorySessionStore()
	require.NoError(t, s.Put(context.Background(), "id", oauth2.Session{UserID: "u"}, 10*time.Millisecond))
	_, err := s.Get(context.Background(), "id")
	require.NoError(t, err)
	time.Sleep(30 * time.Millisecond)
	_, err = s.Get(context.Background(), "id")
	require.ErrorIs(t, err, oauth2.ErrSessionNotFound)
}

func TestMemoryStateStore_Expiry(t *testing.T) {
	s := oauth2.NewMemoryStateStore()
	require.NoError(t, s.Put(context.Background(), "k", oauth2.StateEntry{Nonce: "n"}, 10*time.Millisecond))
	_, err := s.Get(context.Background(), "k")
	require.NoError(t, err)
	time.Sleep(30 * time.Millisecond)
	_, err = s.Get(context.Background(), "k")
	require.ErrorIs(t, err, oauth2.ErrStateNotFound)
}
