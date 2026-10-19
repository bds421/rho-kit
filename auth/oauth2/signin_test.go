package oauth2_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
)

// signIn runs the login flow against a fake issuer and returns the callback
// response and the session it stored, if any.
func signIn(t *testing.T, opts ...oauth2.Option) (*httptest.ResponseRecorder, *oauth2.Session) {
	t.Helper()
	issuer := newFakeIssuer(t, "test-client")
	store := oauth2.NewMemorySessionStore()
	client, err := oauth2.NewClient(context.Background(),
		oauth2.Config{Issuer: issuer.server.URL, ClientID: "test-client", RedirectURL: "https://app/cb", Scopes: []string{"profile"}},
		append([]oauth2.Option{oauth2.WithSessionStore(store), oauth2.WithStateStore(oauth2.NewMemoryStateStore()), oauth2.WithInsecureCookie()}, opts...)...,
	)
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle("/oauth/", client.Handlers())
	app := httptest.NewServer(mux)
	t.Cleanup(app.Close)

	httpc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	login, err := httpc.Get(app.URL + "/oauth/login")
	require.NoError(t, err)
	_ = login.Body.Close()
	authURL, err := url.Parse(login.Header.Get("Location"))
	require.NoError(t, err)
	state := authURL.Query().Get("state")
	authorize, err := httpc.Get(authURL.String())
	require.NoError(t, err)
	_ = authorize.Body.Close()

	req := httptest.NewRequest(http.MethodGet, "/oauth/callback?code=code-"+state+"&state="+state, nil)
	for _, c := range login.Cookies() {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.Name == "kit_oauth_session" && c.Value != "" {
			sess, err := store.Get(context.Background(), c.Value)
			require.NoError(t, err)
			return rec, &sess
		}
	}
	return rec, nil
}

func TestSignInHook_AttributesAreKeptOnTheSession(t *testing.T) {
	var seen oauth2.Session
	rec, sess := signIn(t, oauth2.WithSignInHook(func(_ context.Context, s oauth2.Session) (map[string]string, error) {
		seen = s
		return map[string]string{"directory_user": "u-1"}, nil
	}))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.NotNil(t, sess)
	require.Equal(t, "u-1", sess.Attributes["directory_user"])
	require.Equal(t, "user-123", seen.UserID)
	require.NotEmpty(t, seen.IDToken.RevealString(), "the hook sees the raw ID token")
}

func TestSignInHook_RefusalCreatesNoSession(t *testing.T) {
	cases := map[string]struct {
		err  error
		want int
	}{
		"refused": {fmt.Errorf("suspended: %w", oauth2.ErrSignInRefused), http.StatusForbidden},
		"failed":  {errors.New("directory unreachable"), http.StatusBadGateway},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec, sess := signIn(t, oauth2.WithSignInHook(func(context.Context, oauth2.Session) (map[string]string, error) {
				return nil, tc.err
			}))
			require.Equal(t, tc.want, rec.Code)
			require.Nil(t, sess)
			require.NotContains(t, rec.Body.String(), "unreachable", "hook errors stay out of the response")
		})
	}
}
