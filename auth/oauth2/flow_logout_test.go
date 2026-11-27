package oauth2_test

import (
	"context"
	"errors"
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
	flow := newFlow(t, iss)
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
			_, err := flow.VerifyLogoutToken(context.Background(), cfg, raw)
			require.ErrorIs(t, err, oauth2.ErrLogoutTokenInvalid)
		})
	}
}

type failingReplay struct{}

func (failingReplay) Remember(context.Context, string, time.Duration) (bool, error) {
	return false, errors.New("cache down")
}

func TestFlow_VerifyLogoutTokenFailsClosed(t *testing.T) {
	iss := oauth2test.NewIssuer(t, "app", "s3cret", flowCallback)
	flow := newFlow(t, iss, oauth2.WithFlowReplayCache(failingReplay{}))
	_, err := flow.VerifyLogoutToken(context.Background(), flowConfig(iss, "s3cret"), iss.LogoutToken(t, map[string]any{"sub": "abc"}))
	require.Error(t, err)
	assert.NotErrorIs(t, err, oauth2.ErrLogoutTokenInvalid, "a cache failure is not the token's fault")
	assert.Panics(t, func() { oauth2.WithFlowReplayCache(nil) })
}
