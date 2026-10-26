package oauth2_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
)

func TestLogin_ForwardsValidatedStepUpParameters(t *testing.T) {
	_, client := newLifetimeClient(t, oauth2.NewMemorySessionStore())
	res := httptest.NewRecorder()
	client.Handlers().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/oauth/login?prompt=login+consent&max_age=0", nil))
	require.Equal(t, http.StatusFound, res.Code)
	u, err := url.Parse(res.Header().Get("Location"))
	require.NoError(t, err)
	assert.Equal(t, "login consent", u.Query().Get("prompt"))
	assert.Equal(t, "0", u.Query().Get("max_age"))

	// Without parameters nothing is added.
	res = httptest.NewRecorder()
	client.Handlers().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/oauth/login", nil))
	u, err = url.Parse(res.Header().Get("Location"))
	require.NoError(t, err)
	assert.False(t, u.Query().Has("prompt"))
	assert.False(t, u.Query().Has("max_age"))
}

func TestLogin_ForwardsCreateWithoutNamingTheSignedInPerson(t *testing.T) {
	store := oauth2.NewMemorySessionStore()
	_, client := newLifetimeClient(t, store)
	seedSession(t, store, oauth2.Session{SessionID: "s1", Claims: map[string]any{"preferred_username": "jdoe@corp.example"}})
	u := loginLocation(t, client, "?prompt=create", "s1")
	assert.Equal(t, "create", u.Query().Get("prompt"))
	assert.False(t, u.Query().Has("login_hint"), "a new account is not the signed-in person's")
	assert.Equal(t, "new@example.com", loginLocation(t, client, "?prompt=create&login_hint=new%40example.com", "").Query().Get("login_hint"))
}

func TestLogin_RejectsInvalidStepUpParameters(t *testing.T) {
	_, client := newLifetimeClient(t, oauth2.NewMemorySessionStore())
	for _, q := range []string{
		"prompt=admin", "prompt=none+login", "prompt=none+create", "prompt=create+create", "prompt=login+login", "prompt=", "prompt=login%0d",
		"max_age=-1", "max_age=abc", "max_age=01", "max_age=1e3", "max_age=99999999999", "max_age=",
	} {
		res := httptest.NewRecorder()
		client.Handlers().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/oauth/login?"+q, nil))
		assert.Equal(t, http.StatusBadRequest, res.Code, q)
		assert.Empty(t, res.Result().Cookies(), "no state is issued for %s", q)
	}
}

func TestCallback_ExposesAMRAndAuthTime(t *testing.T) {
	store := oauth2.NewMemorySessionStore()
	issuer, client := newLifetimeClient(t, store)
	authTime := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	issuer.idTokenExtra = map[string]any{"amr": []string{"pwd", "otp"}, "auth_time": authTime.Unix()}
	resp := runLogin(t, client, "")
	require.Equal(t, http.StatusOK, resp.Code, "without max_age an old auth_time is accepted")
	sess, err := store.Get(context.Background(), sessionCookieFrom(t, resp).Value)
	require.NoError(t, err)
	assert.Equal(t, []string{"pwd", "otp"}, sess.AMR)
	assert.True(t, authTime.Equal(sess.AuthTime))
}

func TestCallback_MaxAgeRequiresFreshAuthTime(t *testing.T) {
	cases := map[string]struct {
		extra map[string]any
		query string
		ok    bool
	}{
		"fresh auth_time":            {extra: map[string]any{"auth_time": time.Now().Unix()}, query: "?prompt=login&max_age=0", ok: true},
		"within max_age":             {extra: map[string]any{"auth_time": time.Now().Add(-4 * time.Minute).Unix()}, query: "?max_age=300", ok: true},
		"stale auth_time":            {extra: map[string]any{"auth_time": time.Now().Add(-10 * time.Minute).Unix()}, query: "?prompt=login&max_age=0"},
		"older than max_age":         {extra: map[string]any{"auth_time": time.Now().Add(-10 * time.Minute).Unix()}, query: "?max_age=300"},
		"missing auth_time":          {query: "?max_age=0"},
		"malformed auth_time":        {extra: map[string]any{"auth_time": "yesterday"}, query: "?max_age=0"},
		"prompt only is not checked": {extra: map[string]any{"auth_time": time.Now().Add(-time.Hour).Unix()}, query: "?prompt=login", ok: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store := oauth2.NewMemorySessionStore()
			issuer, client := newLifetimeClient(t, store)
			issuer.idTokenExtra = tc.extra
			resp := runLogin(t, client, tc.query)
			if tc.ok {
				require.Equal(t, http.StatusOK, resp.Code, resp.Body.String())
				return
			}
			assert.Equal(t, http.StatusBadRequest, resp.Code)
			assertErrorPage(t, resp)
			for _, ck := range resp.Result().Cookies() {
				assert.NotEqual(t, "kit_oauth_session", ck.Name, "no session for a stale step-up")
			}
		})
	}
}

func TestTokenRefresh_UpdatesAuthenticationClaims(t *testing.T) {
	var f *refreshFixture
	authTime := time.Now().Add(-time.Minute).Truncate(time.Second)
	f = newRefreshFixture(t, time.Now().Add(-time.Minute), func(w http.ResponseWriter, _ *http.Request) {
		writeTokens(t, w, map[string]any{
			"access_token": "access-2", "token_type": "Bearer", "expires_in": 3600,
			"id_token": f.issuer.signClaims(t, "user-123", map[string]any{"amr": []string{"hwk"}, "auth_time": authTime.Unix()}),
		})
	}, oauth2.WithTokenRefresh(time.Minute))
	sess, err := f.session(t)
	require.NoError(t, err)
	assert.Equal(t, []string{"hwk"}, sess.AMR)
	assert.True(t, authTime.Equal(sess.AuthTime))
}

func loginLocation(t *testing.T, client *oauth2.Client, query, session string) *url.URL {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/oauth/login"+query, nil)
	if session != "" {
		req.AddCookie(&http.Cookie{Name: "kit_oauth_session", Value: session})
	}
	res := httptest.NewRecorder()
	client.Handlers().ServeHTTP(res, req)
	require.Equal(t, http.StatusFound, res.Code)
	u, err := url.Parse(res.Header().Get("Location"))
	require.NoError(t, err)
	return u
}

func TestLogin_ForwardsAValidatedLoginHint(t *testing.T) {
	_, client := newLifetimeClient(t, oauth2.NewMemorySessionStore())
	assert.Equal(t, "@example.com", loginLocation(t, client, "?login_hint=%40example.com", "").Query().Get("login_hint"))
	for _, q := range []string{"login_hint=", "login_hint=a+b", "login_hint=a%0db", "login_hint=" + strings.Repeat("a", 255)} {
		res := httptest.NewRecorder()
		client.Handlers().ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/oauth/login?"+q, nil))
		assert.Equal(t, http.StatusBadRequest, res.Code, q)
	}
}

func TestLogin_AStepUpNamesTheSignedInPerson(t *testing.T) {
	store := oauth2.NewMemorySessionStore()
	_, client := newLifetimeClient(t, store)
	seedSession(t, store, oauth2.Session{SessionID: "s1", Claims: map[string]any{"preferred_username": "jdoe@corp.example"}})
	assert.Equal(t, "jdoe@corp.example", loginLocation(t, client, "?prompt=login&max_age=0", "s1").Query().Get("login_hint"))
	assert.False(t, loginLocation(t, client, "", "s1").Query().Has("login_hint"), "a plain login names no one")
	assert.False(t, loginLocation(t, client, "?prompt=login", "").Query().Has("login_hint"), "without a session no one is named")
	assert.Equal(t, "@corp.example", loginLocation(t, client, "?prompt=login&login_hint=%40corp.example", "s1").Query().Get("login_hint"))
}
