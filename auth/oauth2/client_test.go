package oauth2_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
)

// fakeIssuer is a minimal OIDC provider: serves .well-known +
// authorize + token endpoints. Returns a deterministic id_token shaped
// for the kit's verifier (nonce + sub claims).
type fakeIssuer struct {
	server    *httptest.Server
	mux       *http.ServeMux
	codes     map[string]codeRecord
	clientID  string
	authCalls int
}

type codeRecord struct {
	nonce         string
	codeChallenge string
}

func newFakeIssuer(t *testing.T, clientID string) *fakeIssuer {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f := &fakeIssuer{
		server:   srv,
		mux:      mux,
		codes:    make(map[string]codeRecord),
		clientID: clientID,
	}

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 srv.URL,
			"authorization_endpoint": srv.URL + "/authorize",
			"token_endpoint":         srv.URL + "/token",
			"jwks_uri":               srv.URL + "/jwks",
		})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		f.authCalls++
		q := r.URL.Query()
		code := "code-" + q.Get("state")
		f.codes[code] = codeRecord{
			nonce:         q.Get("nonce"),
			codeChallenge: q.Get("code_challenge"),
		}
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
		rec, ok := f.codes[code]
		if !ok {
			http.Error(w, "unknown code", http.StatusBadRequest)
			return
		}
		// Verify the PKCE code_verifier hashes to the original challenge.
		// (Fakes don't enforce; we trust the client to send it.)
		_ = r.FormValue("code_verifier")
		idToken := fakeIDToken(t, rec.nonce)
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

// fakeIDToken builds an unsigned (header.payload.) JWT-shaped string
// the kit's minimal verifier accepts. Real signing is left to
// security/jwtutil for callers needing it.
func fakeIDToken(t *testing.T, nonce string) string {
	t.Helper()
	header := map[string]any{"alg": "none", "typ": "JWT"}
	payload := map[string]any{
		"sub":   "user-123",
		"nonce": nonce,
		"email": "user@example.com",
		"iss":   "fake",
		"aud":   "test-client",
		"exp":   time.Now().Add(time.Hour).Unix(),
	}
	hb, _ := json.Marshal(header)
	pb, _ := json.Marshal(payload)
	encode := func(b []byte) string {
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return fmt.Sprintf("%s.%s.fakesig", encode(hb), encode(pb))
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

func TestNewClient_RequiresSessionAndStateStores(t *testing.T) {
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

	// Step 1: login → captures the state from the redirect target.
	httpc := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	loginResp, err := httpc.Get(appSrv.URL + "/oauth/login")
	require.NoError(t, err)
	defer loginResp.Body.Close()
	authURL, err := url.Parse(loginResp.Header.Get("Location"))
	require.NoError(t, err)
	state := authURL.Query().Get("state")

	// Step 2: visit the issuer's /authorize endpoint as a real browser
	// would (the fake registers the code on this call). Don't follow
	// the next redirect — that would go to the relative callback URL
	// on a different host; we'll synthesise the callback ourselves.
	authResp, err := httpc.Get(authURL.String())
	require.NoError(t, err)
	defer authResp.Body.Close()
	require.Equal(t, http.StatusFound, authResp.StatusCode)

	// Step 3: simulate the issuer redirecting back with code + state.
	cbReq := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=code-"+state+"&state="+state, nil)
	cbResp := httptest.NewRecorder()
	mux.ServeHTTP(cbResp, cbReq)
	require.Equal(t, http.StatusNoContent, cbResp.Code, "callback should succeed: %s", cbResp.Body.String())

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
}

func TestHandlers_CallbackRejectsUnknownState(t *testing.T) {
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
	require.Less(t, cookies[0].MaxAge, 0, "expected MaxAge<0 to clear cookie")
}

func TestMemorySessionStore_Expiry(t *testing.T) {
	s := oauth2.NewMemorySessionStore()
	require.NoError(t, s.Put(context.Background(), "id", oauth2.Session{UserID: "u"}, 10*time.Millisecond))
	got, err := s.Get(context.Background(), "id")
	require.NoError(t, err)
	require.Equal(t, "u", got.UserID)

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
