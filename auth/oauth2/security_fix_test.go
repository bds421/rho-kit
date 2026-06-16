package oauth2_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/bds421/rho-kit/core/v2/secret"
)

// runLoginAndAuthorize drives a login + /authorize round-trip against
// the fake issuer and returns the issued state token.
func runLoginAndAuthorize(t *testing.T, mux *http.ServeMux, loginPath string) (state string) {
	t.Helper()
	appSrv := httptest.NewServer(mux)
	t.Cleanup(appSrv.Close)
	httpc := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	loginResp, err := httpc.Get(appSrv.URL + loginPath)
	require.NoError(t, err)
	defer func() { _ = loginResp.Body.Close() }()
	require.Equal(t, http.StatusFound, loginResp.StatusCode)
	authURL, err := url.Parse(loginResp.Header.Get("Location"))
	require.NoError(t, err)
	state = authURL.Query().Get("state")
	require.NotEmpty(t, state)
	authResp, err := httpc.Get(authURL.String())
	require.NoError(t, err)
	defer func() { _ = authResp.Body.Close() }()
	return state
}

// TestHandlers_OpenRedirectRejected covers finding 1: a post-login
// redirect_to pointing at an off-host absolute URL must NOT be honoured
// verbatim by the callback (open redirect / phishing vector).
func TestHandlers_OpenRedirectRejected(t *testing.T) {
	issuer := newFakeIssuer(t, "test-client")
	client, err := oauth2.NewClient(context.Background(),
		oauth2.Config{
			Issuer: issuer.server.URL, ClientID: "test-client",
			RedirectURL: "https://app/cb", Scopes: []string{"profile"},
		},
		oauth2.WithSessionStore(oauth2.NewMemorySessionStore()),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithInsecureCookie(),
	)
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle("/oauth/", client.Handlers())

	state := runLoginAndAuthorize(t, mux, "/oauth/login?redirect_to="+url.QueryEscape("https://evil.example.com/steal"))

	cbReq := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=code-"+state+"&state="+state, nil)
	cbResp := httptest.NewRecorder()
	mux.ServeHTTP(cbResp, cbReq)

	// A session is still established, but we must never redirect to the
	// attacker host. The callback should fall back to 204 (no deep link)
	// rather than emit a Location header pointing at evil.example.com.
	if loc := cbResp.Header().Get("Location"); loc != "" {
		require.NotContains(t, loc, "evil.example.com",
			"open redirect: attacker-controlled host honoured in Location")
	}
	require.NotEqual(t, http.StatusFound, cbResp.Code,
		"callback must not 302-redirect to an off-host target")
}

// TestHandlers_SafeRelativeRedirectHonoured ensures a legitimate
// same-origin relative redirect_to still works after the fix.
func TestHandlers_SafeRelativeRedirectHonoured(t *testing.T) {
	issuer := newFakeIssuer(t, "test-client")
	client, err := oauth2.NewClient(context.Background(),
		oauth2.Config{
			Issuer: issuer.server.URL, ClientID: "test-client",
			RedirectURL: "https://app/cb", Scopes: []string{"profile"},
		},
		oauth2.WithSessionStore(oauth2.NewMemorySessionStore()),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithInsecureCookie(),
	)
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle("/oauth/", client.Handlers())

	state := runLoginAndAuthorize(t, mux, "/oauth/login?redirect_to="+url.QueryEscape("/dashboard?tab=billing"))

	cbReq := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=code-"+state+"&state="+state, nil)
	cbResp := httptest.NewRecorder()
	mux.ServeHTTP(cbResp, cbReq)

	require.Equal(t, http.StatusFound, cbResp.Code, "body: %s", cbResp.Body.String())
	require.Equal(t, "/dashboard?tab=billing", cbResp.Header().Get("Location"))
}

// TestHandlers_SchemeRelativeRedirectRejected ensures "//evil" style
// scheme-relative targets (which browsers treat as absolute) are not
// honoured.
func TestHandlers_SchemeRelativeRedirectRejected(t *testing.T) {
	issuer := newFakeIssuer(t, "test-client")
	client, err := oauth2.NewClient(context.Background(),
		oauth2.Config{
			Issuer: issuer.server.URL, ClientID: "test-client",
			RedirectURL: "https://app/cb", Scopes: []string{"profile"},
		},
		oauth2.WithSessionStore(oauth2.NewMemorySessionStore()),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithInsecureCookie(),
	)
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle("/oauth/", client.Handlers())

	state := runLoginAndAuthorize(t, mux, "/oauth/login?redirect_to="+url.QueryEscape("//evil.example.com/path"))

	cbReq := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=code-"+state+"&state="+state, nil)
	cbResp := httptest.NewRecorder()
	mux.ServeHTTP(cbResp, cbReq)

	if loc := cbResp.Header().Get("Location"); loc != "" {
		require.NotContains(t, loc, "evil.example.com",
			"scheme-relative open redirect honoured")
	}
	require.NotEqual(t, http.StatusFound, cbResp.Code)
}

// TestHandlers_CallbackProviderErrorNotReflected ensures the callback
// does not echo the attacker-influenced `error` / `error_description`
// query parameters (RFC 6749 §4.1.2.1) back into the response body. The
// caller receives only the opaque sentinel; the raw values are logged
// server-side.
func TestHandlers_CallbackProviderErrorNotReflected(t *testing.T) {
	issuer := newFakeIssuer(t, "test-client")
	client, err := oauth2.NewClient(context.Background(),
		oauth2.Config{
			Issuer: issuer.server.URL, ClientID: "test-client",
			RedirectURL: "https://app/cb",
		},
		oauth2.WithSessionStore(oauth2.NewMemorySessionStore()),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithInsecureCookie(),
	)
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle("/oauth/", client.Handlers())

	const marker = "PWNED-REFLECTED-MARKER-9zX"
	cbReq := httptest.NewRequest(http.MethodGet,
		"/oauth/callback?error=access_denied&error_description="+url.QueryEscape("attacker text "+marker), nil)
	cbResp := httptest.NewRecorder()
	mux.ServeHTTP(cbResp, cbReq)

	require.Equal(t, http.StatusBadRequest, cbResp.Code)
	body := cbResp.Body.String()
	require.Contains(t, body, oauth2.ErrProviderError.Error(),
		"callback must return the opaque provider-error sentinel")
	require.NotContains(t, body, marker,
		"attacker-controlled error_description reflected into response body")
	require.NotContains(t, body, "access_denied",
		"attacker-controlled error parameter reflected into response body")
}

// TestWithoutPKCEPublicClientRejected covers finding 3: WithoutPKCE on
// a public client (no client secret) must fail closed — that config has
// neither PKCE nor a client secret and is forbidden by RFC 7636.
func TestWithoutPKCEPublicClientRejected(t *testing.T) {
	issuer := newFakeIssuer(t, "c")
	_, err := oauth2.NewClient(context.Background(),
		oauth2.Config{Issuer: issuer.server.URL, ClientID: "c", RedirectURL: "https://app/cb"},
		oauth2.WithSessionStore(oauth2.NewMemorySessionStore()),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithoutPKCE(),
	)
	require.Error(t, err, "public client + WithoutPKCE must be rejected")
}

// TestWithoutPKCEConfidentialClientAllowed ensures a confidential
// client (with a non-empty secret) may still disable PKCE.
func TestWithoutPKCEConfidentialClientAllowed(t *testing.T) {
	issuer := newFakeIssuer(t, "c")
	_, err := oauth2.NewClient(context.Background(),
		oauth2.Config{
			Issuer:       issuer.server.URL,
			ClientID:     "c",
			ClientSecret: secret.NewFromString("super-secret"),
			RedirectURL:  "https://app/cb",
		},
		oauth2.WithSessionStore(oauth2.NewMemorySessionStore()),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithoutPKCE(),
	)
	require.NoError(t, err, "confidential client may disable PKCE")
}

// TestHandlers_CallbackVerifyErrorNotLeaked covers finding 4: when the
// ID-token verification fails, the raw verifier error (which can embed
// claims, signatures, or endpoint bodies) must not be echoed into the
// HTTP response body.
func TestHandlers_CallbackVerifyErrorNotLeaked(t *testing.T) {
	// Issuer whose /token returns an id_token signed by a DIFFERENT key
	// than the one published in JWKS, so go-oidc's verifier rejects the
	// signature after a successful code exchange.
	issuer := newFakeIssuer(t, "test-client")
	wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	issuer.mux.HandleFunc("/token-badsig", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		code := r.FormValue("code")
		issuer.mu.Lock()
		rec, ok := issuer.codes[code]
		issuer.mu.Unlock()
		if !ok {
			http.Error(w, "unknown code", http.StatusBadRequest)
			return
		}
		signer, serr := jose.NewSigner(
			jose.SigningKey{Algorithm: jose.RS256, Key: wrongKey},
			(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key-1"),
		)
		require.NoError(t, serr)
		claims := map[string]any{
			"iss":   issuer.server.URL,
			"sub":   "user-123",
			"aud":   "test-client",
			"exp":   time.Now().Add(time.Hour).Unix(),
			"iat":   time.Now().Unix(),
			"nonce": rec.nonce,
		}
		raw, jerr := jwt.Signed(signer).Claims(claims).Serialize()
		require.NoError(t, jerr)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"a","token_type":"Bearer","expires_in":3600,"id_token":"` + raw + `"}`))
	})

	client, err := oauth2.NewClient(context.Background(),
		oauth2.Config{
			Issuer: issuer.server.URL, ClientID: "test-client",
			RedirectURL: "https://app/cb",
		},
		oauth2.WithSessionStore(oauth2.NewMemorySessionStore()),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithInsecureCookie(),
	)
	require.NoError(t, err)
	// Point the token endpoint at the bad-signature handler.
	client.OAuth2Config().Endpoint.TokenURL = issuer.server.URL + "/token-badsig"

	mux := http.NewServeMux()
	mux.Handle("/oauth/", client.Handlers())
	state := runLoginAndAuthorize(t, mux, "/oauth/login")

	cbReq := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=code-"+state+"&state="+state, nil)
	cbResp := httptest.NewRecorder()
	mux.ServeHTTP(cbResp, cbReq)

	require.Equal(t, http.StatusBadRequest, cbResp.Code)
	body := cbResp.Body.String()
	require.Contains(t, body, oauth2.ErrCodeExchange.Error())
	// The raw verifier error mentions signature / verification details;
	// none of that should cross the trust boundary into the body.
	lower := strings.ToLower(body)
	for _, leak := range []string{"signature", "jwks", "failed to verify", "oidc:", "x509", "crypto/rsa"} {
		require.NotContainsf(t, lower, leak,
			"verifier error detail %q leaked into response body: %s", leak, body)
	}
}
