package oauth2_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPathPrefixSignInAndRecovery(t *testing.T) {
	issuer := newFakeIssuer(t, "test-client")
	client, err := oauth2.NewClient(t.Context(), oauth2.Config{Issuer: issuer.server.URL, ClientID: "test-client", RedirectURL: "https://app/accounts/oauth/callback"},
		oauth2.WithSessionStore(oauth2.NewMemorySessionStore()), oauth2.WithStateStore(oauth2.NewMemoryStateStore()), oauth2.WithPathPrefix("/accounts/oauth"))
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle(client.PathPrefix()+"/", client.Handlers())
	state, cookies := runLoginAndAuthorize(t, mux, "/accounts/oauth/login?redirect_to=%2Faccounts%2F")
	for _, cookie := range cookies {
		assert.Equal(t, "/accounts/oauth/", cookie.Path)
	}
	callback := httptest.NewRequest(http.MethodGet, "/accounts/oauth/callback?code=code-"+state+"&state="+state, nil)
	addCookies(callback, cookies)
	result := httptest.NewRecorder()
	mux.ServeHTTP(result, callback)
	require.Equal(t, http.StatusFound, result.Code, result.Body.String())
	assert.Equal(t, "/accounts/", result.Header().Get("Location"))
	for _, cookie := range result.Result().Cookies() {
		if cookie.MaxAge == -1 {
			assert.Equal(t, "/accounts/oauth/", cookie.Path)
		}
	}
	recovery := httptest.NewRecorder()
	mux.ServeHTTP(recovery, httptest.NewRequest(http.MethodGet, "/accounts/oauth/callback?state=unknown", nil))
	assert.Equal(t, "/accounts/oauth/login?redirect_to=%2F", recovery.Header().Get("Location"))
	retry := httptest.NewRequest(http.MethodGet, "/accounts/oauth/callback?state=unknown", nil)
	addCookies(retry, recovery.Result().Cookies())
	failure := httptest.NewRecorder()
	mux.ServeHTTP(failure, retry)
	assert.Contains(t, failure.Body.String(), `href="/accounts/oauth/login?redirect_to=`)
}

func TestPathPrefixRejectsAmbiguousPaths(t *testing.T) {
	for _, prefix := range []string{"", "oauth", "/", "//other", "/accounts/", "/a/../oauth", "/a?b", "/a#b", "/a;b", "/a b", "/a%2Fb", "https://other/oauth"} {
		t.Run(prefix, func(t *testing.T) { assert.Panics(t, func() { oauth2.WithPathPrefix(prefix) }) })
	}
}
