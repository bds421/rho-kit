package oauth2_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/bds421/rho-kit/auth/oauth2/v2/oauth2test"
)

// mixUp starts a sign-in with a and lets b answer it: an attacker sends
// the browser to b's authorization endpoint with a's state.
func mixUp(t *testing.T, flow *oauth2.Flow, a oauth2.Config, b *oauth2test.Issuer, bCfg oauth2.Config) *http.Request {
	t.Helper()
	start := httptest.NewRecorder()
	authURL, err := flow.Start(start, httptest.NewRequest(http.MethodPost, "/start", nil), oauth2.StartRequest{Config: a, Key: "a"})
	require.NoError(t, err)
	fromA, err := url.Parse(authURL)
	require.NoError(t, err)
	q := fromA.Query()
	q.Set("client_id", bCfg.ClientID)
	q.Set("redirect_uri", bCfg.RedirectURL)
	toB := b.URL() + "/authorize?" + q.Encode()
	client := b.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Get(toB)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	back, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	callback := httptest.NewRequest(http.MethodGet, back.RequestURI(), nil)
	for _, c := range start.Result().Cookies() {
		callback.AddCookie(c)
	}
	return callback
}

func keyed(configs map[string]oauth2.Config) oauth2.Resolver {
	return func(_ context.Context, key string) (oauth2.Config, error) { return configs[key], nil }
}

func TestFlow_RefusesAnAnswerFromAnotherProvider(t *testing.T) {
	a := oauth2test.NewIssuer(t, "app", "", "https://app.example.com/callback/a")
	aCfg := oauth2.Config{Issuer: a.URL(), ClientID: "app", RedirectURL: "https://app.example.com/callback/a"}

	t.Run("at another callback URL", func(t *testing.T) {
		b := oauth2test.NewIssuer(t, "app", "", "https://app.example.com/callback/b")
		bCfg := oauth2.Config{Issuer: b.URL(), ClientID: "app", RedirectURL: "https://app.example.com/callback/b"}
		flow := newFlow(t, a)
		callback := mixUp(t, flow, aCfg, b, bCfg)
		_, err := flow.Finish(httptest.NewRecorder(), callback, keyed(map[string]oauth2.Config{"a": aCfg, "b": bCfg}))
		assert.ErrorIs(t, err, oauth2.ErrIssuerMismatch)
	})
	t.Run("naming itself in iss", func(t *testing.T) {
		a.SendIssuer(true)
		defer a.SendIssuer(false)
		b := oauth2test.NewIssuer(t, "app", "", aCfg.RedirectURL)
		b.SendIssuer(true)
		bCfg := oauth2.Config{Issuer: b.URL(), ClientID: "app", RedirectURL: aCfg.RedirectURL}
		flow := newFlow(t, a)
		callback := mixUp(t, flow, aCfg, b, bCfg)
		_, err := flow.Finish(httptest.NewRecorder(), callback, keyed(map[string]oauth2.Config{"a": aCfg}))
		assert.ErrorIs(t, err, oauth2.ErrIssuerMismatch)
	})
	t.Run("when the key names another issuer now", func(t *testing.T) {
		b := oauth2test.NewIssuer(t, "app", "", aCfg.RedirectURL)
		flow := newFlow(t, a)
		callback := flowSignIn(t, a, flow, oauth2.StartRequest{Config: aCfg, Key: "a"})
		moved := aCfg
		moved.Issuer = b.URL()
		_, err := flow.Finish(httptest.NewRecorder(), callback, keyed(map[string]oauth2.Config{"a": moved}))
		assert.ErrorIs(t, err, oauth2.ErrIssuerMismatch)
	})
}

func TestFlow_IssParameterWhenTheProviderSendsIt(t *testing.T) {
	iss := oauth2test.NewIssuer(t, "app", "", flowCallback)
	iss.SendIssuer(true)
	cfg := flowConfig(iss, "")
	resolve := resolverFor(cfg)

	callback := flowSignIn(t, iss, newFlow(t, iss), oauth2.StartRequest{Config: cfg, Key: "p1"})
	assert.Equal(t, iss.URL(), callback.URL.Query().Get("iss"))

	for name, edit := range map[string]func(url.Values){
		"missing": func(q url.Values) { q.Del("iss") },
		"wrong":   func(q url.Values) { q.Set("iss", iss.URL()+"/other") },
	} {
		t.Run(name, func(t *testing.T) {
			flow := newFlow(t, iss)
			callback := flowSignIn(t, iss, flow, oauth2.StartRequest{Config: cfg, Key: "p1"})
			q := callback.URL.Query()
			edit(q)
			callback.URL.RawQuery = q.Encode()
			_, err := flow.Finish(httptest.NewRecorder(), callback, resolve)
			assert.ErrorIs(t, err, oauth2.ErrIssuerMismatch)
		})
	}
	t.Run("right", func(t *testing.T) {
		flow := newFlow(t, iss)
		callback := flowSignIn(t, iss, flow, oauth2.StartRequest{Config: cfg, Key: "p1"})
		_, err := flow.Finish(httptest.NewRecorder(), callback, resolve)
		require.NoError(t, err)
	})
}
