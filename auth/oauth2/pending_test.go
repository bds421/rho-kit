package oauth2_test

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pendingCookie mirrors the browser binding /login sets for state.
func pendingCookie(state string) *http.Cookie {
	sum := sha256.Sum256([]byte(state))
	binding := base64.RawURLEncoding.EncodeToString(sum[:])
	return &http.Cookie{
		Name:  "kit_oauth_session_state_" + binding[:16],
		Value: strconv.FormatInt(time.Now().UnixNano(), 10) + "." + binding,
	}
}

func assertErrorPage(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), `href="/oauth/login?redirect_to=`)
	assert.NotContains(t, rec.Body.String(), "oauth2:")
	assertNoSession(t, rec)
}

// assertRestart checks a callback answered by restarting the sign-in towards
// returnTo, without exchanging a code or creating a session.
func assertRestart(t *testing.T, rec *httptest.ResponseRecorder, returnTo string) {
	t.Helper()
	require.Equal(t, http.StatusFound, rec.Code, rec.Body.String())
	assert.Equal(t, "/oauth/login?redirect_to="+url.QueryEscape(returnTo), rec.Header().Get("Location"))
	assertNoSession(t, rec)
}

func assertNoSession(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == "kit_oauth_session" {
			assert.Empty(t, c.Value, "no session may be created")
		}
	}
}
