package oauth2_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/bds421/rho-kit/auth/oauth2/v2/oauth2test"
	"github.com/bds421/rho-kit/core/v2/secret"
)

const flowCallback = "https://app.example.com/callback"

func flowConfig(iss *oauth2test.Issuer, clientSecret string) oauth2.Config {
	return oauth2.Config{
		Issuer: iss.URL(), ClientID: "app", ClientSecret: secret.NewFromString(clientSecret),
		RedirectURL: flowCallback, Scopes: []string{"email"},
	}
}

func newFlow(t *testing.T, iss *oauth2test.Issuer, opts ...oauth2.FlowOption) *oauth2.Flow {
	t.Helper()
	flow, err := oauth2.NewFlow(oauth2.NewMemoryStateStore(), append([]oauth2.FlowOption{oauth2.WithFlowHTTPClient(iss.Client())}, opts...)...)
	require.NoError(t, err)
	return flow
}

// flowSignIn starts a sign-in and follows the provider's redirect, returning
// the callback request with the browser's cookies.
func flowSignIn(t *testing.T, iss *oauth2test.Issuer, flow *oauth2.Flow, req oauth2.StartRequest) *http.Request {
	t.Helper()
	start := httptest.NewRecorder()
	authURL, err := flow.Start(start, httptest.NewRequest(http.MethodPost, "/start", nil), req)
	require.NoError(t, err)
	noFollow := iss.Client()
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := noFollow.Get(authURL)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusFound, resp.StatusCode)
	back, err := url.Parse(resp.Header.Get("Location"))
	require.NoError(t, err)
	callback := httptest.NewRequest(http.MethodGet, back.RequestURI(), nil)
	for _, c := range start.Result().Cookies() {
		callback.AddCookie(c)
	}
	return callback
}

func resolverFor(cfg oauth2.Config) oauth2.Resolver {
	return func(_ context.Context, key string) (oauth2.Config, error) {
		if key != "p1" {
			return oauth2.Config{}, errors.New("unknown provider")
		}
		return cfg, nil
	}
}

func TestFlow_SignInReturnsVerifiedIdentity(t *testing.T) {
	iss := oauth2test.NewIssuer(t, "app", "s3cret", flowCallback)
	iss.SetClaims(map[string]any{"sub": "abc", "email": "a@example.com", "email_verified": true})
	flow := newFlow(t, iss)
	cfg := flowConfig(iss, "s3cret")

	start := httptest.NewRecorder()
	authURL, err := flow.Start(start, httptest.NewRequest(http.MethodPost, "/start", nil), oauth2.StartRequest{Config: cfg, Key: "p1"})
	require.NoError(t, err)
	u, err := url.Parse(authURL)
	require.NoError(t, err)
	assert.Equal(t, "S256", u.Query().Get("code_challenge_method"))
	assert.NotEmpty(t, u.Query().Get("nonce"))
	cookies := start.Result().Cookies()
	require.Len(t, cookies, 1)
	assert.Equal(t, http.SameSiteLaxMode, cookies[0].SameSite)
	assert.True(t, cookies[0].Secure && cookies[0].HttpOnly)
	assert.Equal(t, "/", cookies[0].Path)

	callback := flowSignIn(t, iss, flow, oauth2.StartRequest{Config: cfg, Key: "p1", Data: "link:42", RedirectTo: "/settings"})
	rec := httptest.NewRecorder()
	res, err := flow.Finish(rec, callback, resolverFor(cfg))
	require.NoError(t, err)
	assert.Equal(t, iss.URL(), res.Issuer)
	assert.Equal(t, "abc", res.Subject)
	assert.Equal(t, "p1", res.Key)
	assert.Equal(t, "link:42", res.Data)
	assert.Equal(t, "/settings", res.RedirectTo)
	assert.Equal(t, "a@example.com", res.Claims["email"])
	assert.False(t, res.AuthTime.IsZero())
	assert.Empty(t, rec.Body.String(), "Finish writes no body")
	require.Len(t, rec.Result().Cookies(), 1)
	assert.Equal(t, -1, rec.Result().Cookies()[0].MaxAge, "the binding is cleared")

	_, err = flow.Finish(httptest.NewRecorder(), callback, resolverFor(cfg))
	assert.ErrorIs(t, err, oauth2.ErrStateNotFound, "a state is used once")
}

func TestFlow_FinishRefuses(t *testing.T) {
	iss := oauth2test.NewIssuer(t, "app", "s3cret", flowCallback)
	cfg := flowConfig(iss, "s3cret")

	t.Run("a browser that did not start it", func(t *testing.T) {
		flow := newFlow(t, iss)
		callback := flowSignIn(t, iss, flow, oauth2.StartRequest{Config: cfg, Key: "p1"})
		bare := httptest.NewRequest(http.MethodGet, callback.URL.RequestURI(), nil)
		_, err := flow.Finish(httptest.NewRecorder(), bare, resolverFor(cfg))
		assert.ErrorIs(t, err, oauth2.ErrStateMismatch)
	})
	t.Run("an error from the provider", func(t *testing.T) {
		flow := newFlow(t, iss)
		iss.Deny(true)
		defer iss.Deny(false)
		callback := flowSignIn(t, iss, flow, oauth2.StartRequest{Config: cfg, Key: "p1"})
		_, err := flow.Finish(httptest.NewRecorder(), callback, resolverFor(cfg))
		assert.ErrorIs(t, err, oauth2.ErrProviderError)
	})
	t.Run("a provider removed meanwhile", func(t *testing.T) {
		flow := newFlow(t, iss)
		callback := flowSignIn(t, iss, flow, oauth2.StartRequest{Config: cfg, Key: "gone"})
		_, err := flow.Finish(httptest.NewRecorder(), callback, resolverFor(cfg))
		assert.EqualError(t, err, "unknown provider")
	})
	t.Run("a wrong client secret", func(t *testing.T) {
		flow := newFlow(t, iss)
		callback := flowSignIn(t, iss, flow, oauth2.StartRequest{Config: cfg, Key: "p1"})
		_, err := flow.Finish(httptest.NewRecorder(), callback, resolverFor(flowConfig(iss, "wrong")))
		assert.ErrorIs(t, err, oauth2.ErrCodeExchange)
	})
	t.Run("an expired state", func(t *testing.T) {
		now := time.Now()
		flow := newFlow(t, iss, oauth2.WithFlowStateTTL(time.Minute), oauth2.WithFlowClock(func() time.Time { return now }))
		callback := flowSignIn(t, iss, flow, oauth2.StartRequest{Config: cfg, Key: "p1"})
		now = now.Add(2 * time.Minute)
		_, err := flow.Finish(httptest.NewRecorder(), callback, resolverFor(cfg))
		assert.ErrorIs(t, err, oauth2.ErrStateNotFound)
	})
	t.Run("a client the provider does not know", func(t *testing.T) {
		other := oauth2test.NewIssuer(t, "other", "s3cret", flowCallback)
		flow := newFlow(t, other)
		cfg := flowConfig(other, "s3cret")
		cfg.ClientID = "other"
		callback := flowSignIn(t, other, flow, oauth2.StartRequest{Config: cfg, Key: "p1"})
		cfg.ClientID = "app"
		_, err := flow.Finish(httptest.NewRecorder(), callback, resolverFor(cfg))
		assert.Error(t, err)
	})
}

func TestFlow_StartRefusesBadInput(t *testing.T) {
	iss := oauth2test.NewIssuer(t, "app", "", flowCallback)
	flow := newFlow(t, iss)
	r := httptest.NewRequest(http.MethodPost, "/start", nil)
	_, err := flow.Start(httptest.NewRecorder(), r, oauth2.StartRequest{Config: oauth2.Config{Issuer: iss.URL()}})
	require.Error(t, err)
	_, err = flow.Start(httptest.NewRecorder(), r, oauth2.StartRequest{Config: flowConfig(iss, ""), Prompt: "bogus"})
	require.Error(t, err)
	unreachable := flowConfig(iss, "")
	unreachable.Issuer = "http://127.0.0.1:1"
	_, err = flow.Start(httptest.NewRecorder(), r, oauth2.StartRequest{Config: unreachable})
	require.ErrorIs(t, err, oauth2.ErrIssuerDiscovery)
}

func TestFlow_RedirectToIsRelativeOnly(t *testing.T) {
	iss := oauth2test.NewIssuer(t, "app", "", flowCallback)
	flow := newFlow(t, iss)
	cfg := flowConfig(iss, "")
	callback := flowSignIn(t, iss, flow, oauth2.StartRequest{Config: cfg, Key: "p1", RedirectTo: "//evil.example"})
	res, err := flow.Finish(httptest.NewRecorder(), callback, resolverFor(cfg))
	require.NoError(t, err)
	assert.Empty(t, res.RedirectTo)
}

func TestNewFlow_Options(t *testing.T) {
	_, err := oauth2.NewFlow(nil)
	require.Error(t, err)
	_, err = oauth2.NewFlow(oauth2.NewMemoryStateStore(), oauth2.WithFlowCookie("__Host-x_", "/oidc"))
	require.Error(t, err, "__Host- needs the path /")
	_, err = oauth2.NewFlow(oauth2.NewMemoryStateStore(), oauth2.WithFlowInsecureCookie())
	require.Error(t, err, "__Host- needs Secure")
	_, err = oauth2.NewFlow(oauth2.NewMemoryStateStore(), oauth2.WithFlowCookie("x_", "/"), oauth2.WithFlowInsecureCookie(), oauth2.WithFlowDiscoveryTTL(time.Second))
	require.NoError(t, err)
	_, err = oauth2.NewFlow(oauth2.NewMemoryStateStore(), nil)
	require.Error(t, err)
}
