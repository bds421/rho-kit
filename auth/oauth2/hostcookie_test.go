package oauth2_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHostPrefixedCookiesStayValidForBrowsers(t *testing.T) {
	issuer := newFakeIssuer(t, "test-client")
	client, err := oauth2.NewClient(t.Context(), oauth2.Config{Issuer: issuer.server.URL, ClientID: "test-client", RedirectURL: "https://app/oauth/callback"},
		oauth2.WithSessionStore(oauth2.NewMemorySessionStore()), oauth2.WithStateStore(oauth2.NewMemoryStateStore()), oauth2.WithCookieName("__Host-app_session"))
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle("/oauth/", client.Handlers())
	state, cookies := runLoginAndAuthorize(t, mux, "/oauth/login?redirect_to=%2F")
	callback := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=code-"+state+"&state="+state, nil)
	addCookies(callback, cookies)
	result := httptest.NewRecorder()
	mux.ServeHTTP(result, callback)
	require.Equal(t, http.StatusFound, result.Code, result.Body.String())
	all := append(cookies, result.Result().Cookies()...)
	require.NotEmpty(t, all)
	for _, cookie := range all {
		assert.True(t, strings.HasPrefix(cookie.Name, "__Host-app_session"), cookie.Name)
		assert.Equal(t, "/", cookie.Path, cookie.Name)
		assert.Empty(t, cookie.Domain, cookie.Name)
		assert.True(t, cookie.Secure, cookie.Name)
		assert.True(t, cookie.HttpOnly, cookie.Name)
	}
}

func TestHostPrefixedCookiesRefuseDomainAndInsecure(t *testing.T) {
	issuer := newFakeIssuer(t, "test-client")
	cfg := oauth2.Config{Issuer: issuer.server.URL, ClientID: "test-client", RedirectURL: "https://app/oauth/callback"}
	stores := []oauth2.Option{oauth2.WithSessionStore(oauth2.NewMemorySessionStore()), oauth2.WithStateStore(oauth2.NewMemoryStateStore()), oauth2.WithCookieName("__Host-app_session")}
	_, err := oauth2.NewClient(t.Context(), cfg, append(stores, oauth2.WithCookieDomain("app"))...)
	assert.Error(t, err)
	_, err = oauth2.NewClient(t.Context(), cfg, append(stores, oauth2.WithInsecureCookie())...)
	assert.Error(t, err)
}
