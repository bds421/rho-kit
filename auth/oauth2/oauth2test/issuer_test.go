package oauth2test_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/auth/oauth2/v2/oauth2test"
)

func TestIssuer_DiscoveryNamesItself(t *testing.T) {
	iss := oauth2test.NewIssuer(t, "app", "", "https://a.example/cb")
	resp, err := iss.Client().Get(iss.URL() + "/.well-known/openid-configuration")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	var doc map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&doc))
	assert.Equal(t, iss.URL(), doc["issuer"])
}

func TestIssuer_RefusesUnknownClientAndPlainPKCE(t *testing.T) {
	iss := oauth2test.NewIssuer(t, "app", "", "https://a.example/cb")
	for _, q := range []url.Values{
		{"client_id": {"other"}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "redirect_uri": {"https://a.example/cb"}},
		{"client_id": {"app"}, "response_type": {"code"}, "code_challenge_method": {"plain"}, "redirect_uri": {"https://a.example/cb"}},
	} {
		resp, err := iss.Client().Get(iss.URL() + "/authorize?" + q.Encode())
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	}
}

func TestIssuer_TokenNeedsAKnownCode(t *testing.T) {
	iss := oauth2test.NewIssuer(t, "app", "secret")
	resp, err := iss.Client().PostForm(iss.URL()+"/token", url.Values{"client_id": {"app"}, "client_secret": {"secret"}, "code": {"nope"}})
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, err = iss.Client().PostForm(iss.URL()+"/token", url.Values{"client_id": {"app"}, "client_secret": {"wrong"}})
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
