package oauth2_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
)

// TestNewClient_SurvivesConstructionContextCancel is the regression pin
// for review-06: go-oidc's RemoteKeySet retains the construction context
// for JWKS refresh. NewClient must bind discovery to
// context.WithoutCancel so a caller-side cancel after construction does
// not poison later token verification / JWKS use.
func TestNewClient_SurvivesConstructionContextCancel(t *testing.T) {
	issuer := newFakeIssuer(t, "ctx-cancel-client")

	ctx, cancel := context.WithCancel(context.Background())
	client, err := oauth2.NewClient(ctx,
		oauth2.Config{
			Issuer:      issuer.server.URL,
			ClientID:    "ctx-cancel-client",
			RedirectURL: "https://app.example.com/oauth/callback",
			Scopes:      []string{"profile"},
		},
		oauth2.WithSessionStore(oauth2.NewMemorySessionStore()),
		oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
		oauth2.WithInsecureCookie(),
	)
	require.NoError(t, err)
	// Cancel the construction context — this is the footgun: with the
	// old code every subsequent JWKS refresh would fail with
	// context.Canceled.
	cancel()

	require.NotNil(t, client.Provider())
	require.NotNil(t, client.OAuth2Config())

	// Drive a real login redirect against Handlers after cancel. If the
	// provider were bound to the cancelled ctx, auth URL construction
	// still works (local) — the critical pin is ID-token verify after
	// the IdP round-trip still succeeds.
	mux := http.NewServeMux()
	mux.Handle("/oauth/", client.Handlers())
	appSrv := httptest.NewServer(mux)
	t.Cleanup(appSrv.Close)

	httpc := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	loginResp, err := httpc.Get(appSrv.URL + "/oauth/login")
	require.NoError(t, err)
	defer func() { _ = loginResp.Body.Close() }()
	require.Equal(t, http.StatusFound, loginResp.StatusCode)
	loginCookies := loginResp.Cookies()

	authURL, err := url.Parse(loginResp.Header.Get("Location"))
	require.NoError(t, err)
	state := authURL.Query().Get("state")
	require.NotEmpty(t, state)

	authResp, err := httpc.Get(authURL.String())
	require.NoError(t, err)
	defer func() { _ = authResp.Body.Close() }()
	require.Equal(t, http.StatusFound, authResp.StatusCode)

	cbReq := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=code-"+state+"&state="+state, nil)
	for _, ck := range loginCookies {
		cbReq.AddCookie(ck)
	}
	cbResp := httptest.NewRecorder()
	mux.ServeHTTP(cbResp, cbReq)
	require.Equal(t, http.StatusOK, cbResp.Code,
		"callback after construction-ctx cancel must still verify id_token (JWKS): %s",
		cbResp.Body.String())
}
