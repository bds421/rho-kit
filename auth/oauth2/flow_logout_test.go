package oauth2_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/auth/oauth2/v2"
	"github.com/bds421/rho-kit/auth/oauth2/v2/oauth2test"
)

func TestFlow_VerifyLogoutToken(t *testing.T) {
	iss := oauth2test.NewIssuer(t, "app", "s3cret", flowCallback)
	flow := newFlow(t, iss)
	cfg := flowConfig(iss, "s3cret")
	ctx := context.Background()

	raw := iss.LogoutToken(t, map[string]any{"sub": "abc", "sid": "idp-1"})
	tok, err := flow.VerifyLogoutToken(ctx, cfg, raw)
	require.NoError(t, err)
	assert.Equal(t, oauth2.LogoutToken{Issuer: iss.URL(), Subject: "abc", SID: "idp-1"}, tok)

	_, err = flow.VerifyLogoutToken(ctx, cfg, raw)
	require.ErrorIs(t, err, oauth2.ErrLogoutTokenReplayed, "a jti is used once")

	tok, err = flow.VerifyLogoutToken(ctx, cfg, iss.LogoutToken(t, map[string]any{"sid": "idp-2"}))
	require.NoError(t, err)
	assert.Equal(t, oauth2.LogoutToken{Issuer: iss.URL(), SID: "idp-2"}, tok, "sid alone")
}

func TestFlow_VerifyLogoutTokenRefuses(t *testing.T) {
	iss := oauth2test.NewIssuer(t, "app", "s3cret", flowCallback)
	other := oauth2test.NewIssuer(t, "app", "s3cret", flowCallback)
	cfg := flowConfig(iss, "s3cret")
	cases := map[string]string{
		"other client":          iss.LogoutToken(t, map[string]any{"sub": "abc", "aud": "other"}),
		"other provider's key":  other.LogoutToken(t, map[string]any{"sub": "abc", "iss": iss.URL()}),
		"expired":               iss.LogoutToken(t, map[string]any{"sub": "abc", "exp": time.Now().Add(-time.Minute).Unix()}),
		"no events":             iss.LogoutToken(t, map[string]any{"sub": "abc", "events": nil}),
		"events without logout": iss.LogoutToken(t, map[string]any{"sub": "abc", "events": map[string]any{"urn:example:other": map[string]any{}}}),
		"nonce":                 iss.LogoutToken(t, map[string]any{"sub": "abc", "nonce": "n"}),
		"no sub and no sid":     iss.LogoutToken(t, nil),
		"no jti":                iss.LogoutToken(t, map[string]any{"sub": "abc", "jti": nil}),
		"no iat":                iss.LogoutToken(t, map[string]any{"sub": "abc", "iat": nil}),
		"iat too old":           iss.LogoutToken(t, map[string]any{"sub": "abc", "iat": time.Now().Add(-10 * time.Minute).Unix()}),
		"iat ahead":             iss.LogoutToken(t, map[string]any{"sub": "abc", "iat": time.Now().Add(10 * time.Minute).Unix()}),
		"not a token":           "not-a-jwt",
		"empty":                 "",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			// A flow per case: an unknown key right after another fetch is busy, not invalid.
			flow := newFlow(t, iss)
			_, err := flow.VerifyLogoutToken(context.Background(), cfg, raw)
			require.ErrorIs(t, err, oauth2.ErrLogoutTokenInvalid)
		})
	}
}

type failingReplay struct{}

func (failingReplay) Remember(context.Context, string, time.Duration) (bool, error) {
	return false, errors.New("cache down")
}

func (failingReplay) Forget(context.Context, string) error { return errors.New("cache down") }

func TestFlow_VerifyLogoutTokenFailsClosed(t *testing.T) {
	iss := oauth2test.NewIssuer(t, "app", "s3cret", flowCallback)
	flow := newFlow(t, iss, oauth2.WithFlowReplayCache(failingReplay{}))
	_, err := flow.VerifyLogoutToken(context.Background(), flowConfig(iss, "s3cret"), iss.LogoutToken(t, map[string]any{"sub": "abc"}))
	require.Error(t, err)
	assert.NotErrorIs(t, err, oauth2.ErrLogoutTokenInvalid, "a cache failure is not the token's fault")
	assert.Panics(t, func() { oauth2.WithFlowReplayCache(nil) })
}

// An admission refused after verification forgets the jti, so the
// provider's next delivery of the same token is admitted; neither a
// forged nor a replayed token reaches the admission.
func TestFlow_VerifyLogoutTokenAdmitsVerifiedTokensOnly(t *testing.T) {
	iss := oauth2test.NewIssuer(t, "app", "s3cret", flowCallback)
	other := oauth2test.NewIssuer(t, "app", "s3cret", flowCallback)
	flow := newFlow(t, iss)
	cfg := flowConfig(iss, "s3cret")
	full := errors.New("budget spent")
	var admitted []oauth2.LogoutToken
	admit := func(refuse bool) func(context.Context, oauth2.LogoutToken) error {
		return func(_ context.Context, tok oauth2.LogoutToken) error {
			admitted = append(admitted, tok)
			if refuse {
				return full
			}
			return nil
		}
	}
	_, err := flow.VerifyLogoutToken(context.Background(), cfg, other.LogoutToken(t, map[string]any{"sub": "abc", "iss": iss.URL()}), admit(false))
	require.ErrorIs(t, err, oauth2.ErrLogoutTokenInvalid)
	assert.Empty(t, admitted, "a forged token reached the admission")

	raw := iss.LogoutToken(t, map[string]any{"sub": "abc"})
	_, err = flow.VerifyLogoutToken(context.Background(), cfg, raw, admit(true))
	require.ErrorIs(t, err, full)
	tok, err := flow.VerifyLogoutToken(context.Background(), cfg, raw, admit(false))
	require.NoError(t, err, "a refused admission recorded the jti")
	assert.Equal(t, "abc", tok.Subject)
	_, err = flow.VerifyLogoutToken(context.Background(), cfg, raw, admit(false))
	require.ErrorIs(t, err, oauth2.ErrLogoutTokenReplayed)
	assert.Len(t, admitted, 2, "a replayed token reached the admission")
}

// Forged logout tokens make the provider's keys be fetched at most once a
// refetch interval, and a valid token still verifies from the cached keys.
func TestFlow_LogoutTokensBoundKeyFetches(t *testing.T) {
	iss := oauth2test.NewIssuer(t, "app", "s3cret", flowCallback)
	forger := oauth2test.NewIssuer(t, "app", "s3cret", flowCallback)
	now := time.Now()
	flow := newFlow(t, iss, oauth2.WithFlowClock(func() time.Time { return now }), oauth2.WithFlowKeyRefetchInterval(time.Minute))
	cfg := flowConfig(iss, "s3cret")
	ctx := context.Background()
	_, err := flow.VerifyLogoutToken(ctx, cfg, iss.LogoutToken(t, map[string]any{"sub": "abc"}))
	require.NoError(t, err)
	require.Equal(t, 1, iss.KeyFetches())
	for range 20 {
		_, err := flow.VerifyLogoutToken(ctx, cfg, forger.LogoutToken(t, map[string]any{"sub": "abc", "iss": iss.URL()}))
		busy, ok := errors.AsType[*oauth2.LogoutKeysBusyError](err)
		require.True(t, ok, "a token whose keys may not be fetched yet: %v", err)
		require.ErrorIs(t, err, oauth2.ErrLogoutKeysBusy)
		assert.Equal(t, time.Minute, busy.RetryAfter)
	}
	assert.Equal(t, 1, iss.KeyFetches(), "forged tokens fetched the keys again")
	_, err = flow.VerifyLogoutToken(ctx, cfg, iss.LogoutToken(t, map[string]any{"sub": "abc"}))
	require.NoError(t, err, "a valid token after the flood")
	now = now.Add(time.Minute)
	_, _ = flow.VerifyLogoutToken(ctx, cfg, forger.LogoutToken(t, map[string]any{"sub": "abc", "iss": iss.URL()}))
	assert.Equal(t, 2, iss.KeyFetches(), "a fetch once the interval passed")

	_, err = oauth2.NewFlow(oauth2.NewMemoryStateStore(), oauth2.WithFlowKeyRefetchInterval(0))
	require.Error(t, err)
}

// The logout key set keeps the caller's client: a jwks_uri that redirects
// elsewhere is refused when the caller's redirect policy refuses it (an
// egress guard's), and followed when it allows it.
func TestFlow_LogoutKeysKeepTheClientsRedirectPolicy(t *testing.T) {
	iss := oauth2test.NewIssuer(t, "app", "s3cret", flowCallback)
	var front *httptest.Server
	front = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"issuer":"` + front.URL + `","authorization_endpoint":"` + front.URL + `/authorize","token_endpoint":"` + front.URL + `/token","jwks_uri":"` + front.URL + `/jwks","id_token_signing_alg_values_supported":["RS256"]}`))
		case "/jwks":
			http.Redirect(w, r, iss.URL()+"/jwks", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(front.Close)
	cfg := oauth2.Config{Issuer: front.URL, ClientID: "app", RedirectURL: flowCallback}
	token := func() string { return iss.LogoutToken(t, map[string]any{"sub": "abc", "iss": front.URL}) }

	guarded := *iss.Client() // a copy: the issuer hands out one client
	guarded.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("redirect refused") }
	refusing := newFlow(t, iss, oauth2.WithFlowHTTPClient(&guarded))
	_, err := refusing.VerifyLogoutToken(context.Background(), cfg, token())
	require.ErrorIs(t, err, oauth2.ErrLogoutTokenInvalid)
	assert.Zero(t, iss.KeyFetches(), "the redirect was followed")

	following := newFlow(t, iss)
	_, err = following.VerifyLogoutToken(context.Background(), cfg, token())
	require.NoError(t, err, "the same redirect where the client allows it")
}
