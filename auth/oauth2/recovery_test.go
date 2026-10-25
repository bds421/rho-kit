package oauth2_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
)

type signInRig struct {
	t      *testing.T
	issuer *fakeIssuer
	app    http.Handler
}

func newSignInRig(t *testing.T, opts ...oauth2.Option) *signInRig {
	t.Helper()
	issuer := newFakeIssuer(t, "test-client")
	client, err := oauth2.NewClient(context.Background(),
		oauth2.Config{Issuer: issuer.server.URL, ClientID: "test-client", RedirectURL: "https://app/oauth/callback"},
		append([]oauth2.Option{
			oauth2.WithSessionStore(oauth2.NewMemorySessionStore()),
			oauth2.WithStateStore(oauth2.NewMemoryStateStore()),
			oauth2.WithInsecureCookie(),
		}, opts...)...,
	)
	require.NoError(t, err)
	mux := http.NewServeMux()
	mux.Handle("/oauth/", client.Handlers())
	return &signInRig{t: t, issuer: issuer, app: mux}
}

// browser keeps cookies across requests to the application the way a real
// browser does, so tabs share one cookie jar.
type browser struct {
	rig     *signInRig
	cookies map[string]*http.Cookie
	lang    string
}

func (r *signInRig) browser() *browser {
	return &browser{rig: r, cookies: map[string]*http.Cookie{}}
}

func (b *browser) get(target string) *httptest.ResponseRecorder {
	b.rig.t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if b.lang != "" {
		req.Header.Set("Accept-Language", b.lang)
	}
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	b.rig.app.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 || c.Value == "" {
			delete(b.cookies, c.Name)
			continue
		}
		b.cookies[c.Name] = c
	}
	return rec
}

func (b *browser) hasSession() bool {
	return b.cookies["kit_oauth_session"] != nil
}

// startSignIn opens /oauth/login in a tab and lets the identity provider
// sign the person in; it returns the callback URL the provider redirects to.
func (b *browser) startSignIn(returnTo string) string {
	b.rig.t.Helper()
	login := b.get("/oauth/login?redirect_to=" + url.QueryEscape(returnTo))
	require.Equal(b.rig.t, http.StatusFound, login.Code, login.Body.String())
	return authorize(b.rig.t, login.Header().Get("Location"))
}

func authorize(t *testing.T, authURL string) string {
	t.Helper()
	httpc := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := httpc.Get(authURL)
	require.NoError(t, err)
	_ = resp.Body.Close()
	callback, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	return callback.RequestURI()
}

func (b *browser) dropPendingSignIns() {
	for name := range b.cookies {
		if strings.HasPrefix(name, "kit_oauth_session_state_") {
			delete(b.cookies, name)
		}
	}
}

func (r *signInRig) countTokenCalls() *atomic.Int32 {
	var calls atomic.Int32
	r.issuer.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "unexpected exchange", http.StatusBadRequest)
	}
	return &calls
}

func TestSignIn_TwoTabsConcurrently(t *testing.T) {
	rig := newSignInRig(t)
	b := rig.browser()
	first := b.startSignIn("/cases")
	second := b.startSignIn("/settings")

	res := b.get(first)
	require.Equal(t, http.StatusFound, res.Code, res.Body.String())
	assert.Equal(t, "/cases", res.Header().Get("Location"))
	res = b.get(second)
	require.Equal(t, http.StatusFound, res.Code, res.Body.String())
	assert.Equal(t, "/settings", res.Header().Get("Location"))
	assert.True(t, b.hasSession())
}

func TestSignIn_ExpiredStateRestartsWithReturnURL(t *testing.T) {
	rig := newSignInRig(t, oauth2.WithStateTTL(time.Millisecond))
	b := rig.browser()
	callback := b.startSignIn("/cases/42?tab=notes")
	time.Sleep(5 * time.Millisecond)
	calls := rig.countTokenCalls()

	assertRestart(t, b.get(callback), "/cases/42?tab=notes")
	assert.Zero(t, calls.Load(), "an expired sign-in must not exchange its code")
}

func TestSignIn_StaleTabAfterSessionTimeoutGoesToReturnURL(t *testing.T) {
	rig := newSignInRig(t)
	b := rig.browser()
	stale := b.startSignIn("/cases")
	b.dropPendingSignIns()
	res := b.get(b.startSignIn("/"))
	require.Equal(t, http.StatusFound, res.Code, res.Body.String())
	require.True(t, b.hasSession())

	res = b.get(stale)
	require.Equal(t, http.StatusFound, res.Code, res.Body.String())
	assert.Equal(t, "/cases", res.Header().Get("Location"))
}

func TestSignIn_StaleTabWithoutSessionRestartsOnce(t *testing.T) {
	rig := newSignInRig(t)
	b := rig.browser()
	b.lang = "de-AT,de;q=0.9,en;q=0.5"
	callback := b.startSignIn("/cases")
	b.dropPendingSignIns()

	assertRestart(t, b.get(callback), "/cases")
	res := b.get("/oauth/callback?code=c&state=still-unknown")
	require.Equal(t, http.StatusBadRequest, res.Code)
	assertErrorPage(t, res)
	assert.Contains(t, res.Body.String(), "Erneut anmelden")
	assert.Contains(t, res.Body.String(), `href="/oauth/login?redirect_to=%2F"`)
}

func TestSignIn_ForeignCallbackIsNotCompleted(t *testing.T) {
	rig := newSignInRig(t)
	attacker := rig.browser()
	forged := attacker.startSignIn("/")
	calls := rig.countTokenCalls()

	victim := rig.browser()
	assertRestart(t, victim.get(forged), "/")
	assert.False(t, victim.hasSession())
	assert.Zero(t, calls.Load(), "a callback not bound to this browser must not exchange its code")

	res := attacker.get(forged)
	assert.NotEqual(t, http.StatusOK, res.Code)
	assert.False(t, attacker.hasSession(), "the forged state is spent")
}

func TestSignIn_PendingSignInsAreBounded(t *testing.T) {
	rig := newSignInRig(t)
	b := rig.browser()
	first := b.startSignIn("/first")
	for range 5 {
		b.startSignIn("/later")
	}
	pending := 0
	for name := range b.cookies {
		if strings.HasPrefix(name, "kit_oauth_session_state_") {
			pending++
		}
	}
	assert.Equal(t, 5, pending)
	assertRestart(t, b.get(first), "/first")
}

func TestSignIn_ErrorPageIsBrandedAndTranslated(t *testing.T) {
	rig := newSignInRig(t, oauth2.WithErrorPageBrand(oauth2.ErrorPageBrand{
		Name: "Example", LogoSVG: []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), Accent: "#0f766e",
	}))
	b := rig.browser()
	b.lang = "es-ES,es;q=0.9"
	callback := b.startSignIn("/cases")
	rig.issuer.tokenHandler = func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "server_error", http.StatusInternalServerError)
	}

	res := b.get(callback)
	require.Equal(t, http.StatusBadGateway, res.Code)
	assertErrorPage(t, res)
	body := res.Body.String()
	assert.Contains(t, body, `<html lang="es">`)
	assert.Contains(t, body, "Iniciar sesión de nuevo")
	assert.Contains(t, body, "Example")
	assert.Contains(t, body, `<img src="data:image/svg`)
	assert.Contains(t, body, `href="/oauth/login?redirect_to=%2Fcases"`)
	assert.Contains(t, res.Header().Get("Content-Security-Policy"), "default-src 'none'")
}

func TestSignIn_HomePathReplacesTheRootWithoutReturnURL(t *testing.T) {
	rig := newSignInRig(t, oauth2.WithHomePath("/accounts/"))
	b := rig.browser()

	assertRestart(t, b.get("/oauth/callback?code=c&state=unknown"), "/accounts/")
	res := b.get("/oauth/callback?code=c&state=still-unknown")
	require.Equal(t, http.StatusBadRequest, res.Code)
	assert.Contains(t, res.Body.String(), `href="/oauth/login?redirect_to=%2Faccounts%2F"`)

	res = b.get("/oauth/login?prompt=admin")
	require.Equal(t, http.StatusBadRequest, res.Code)
	assert.Contains(t, res.Body.String(), `href="/oauth/login?redirect_to=%2Faccounts%2F"`)
}

func TestWithHomePath_RefusesPathsLeavingTheOrigin(t *testing.T) {
	for _, p := range []string{"", "accounts", "//evil", "https://evil/", "/a\\b", " /a"} {
		assert.Panics(t, func() { oauth2.WithHomePath(p) }, p)
	}
}
