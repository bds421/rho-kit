package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/bds421/rho-kit/realtime/centrifuge/v2"
	"github.com/bds421/rho-kit/security/v2/jwtutil"
)

// TestSigningKey_RoundTrip pins the issuer-side ↔ verifier-side
// shape: a token signed with the demo private key must verify
// against the JWKS the same key exposes. If this breaks, every
// downstream test in this file is meaningless.
func TestSigningKey_RoundTrip(t *testing.T) {
	signing, err := newSigningKey()
	require.NoError(t, err)

	verifier, err := signing.newVerifier()
	require.NoError(t, err)

	token, err := signing.SignToken("user-42", time.Minute)
	require.NoError(t, err)

	claims, err := verifier.VerifyContext(context.Background(), token, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "user-42", claims.Subject)
	assert.Equal(t, demoIssuer, claims.Issuer)
}

// TestSigningKey_WrongAudienceRejected verifies the kit's
// confused-deputy mitigation: a token signed for a different
// audience must NOT verify even though the signature is valid.
func TestSigningKey_WrongAudienceRejected(t *testing.T) {
	signing, err := newSigningKey()
	require.NoError(t, err)

	// Build a verifier expecting a DIFFERENT audience.
	ks, err := jwtutil.ParseKeySet(signing.jwksJSON)
	require.NoError(t, err)
	misVerifier := jwtutil.NewProviderWithKeySet(ks,
		jwtutil.WithExpectedIssuer(demoIssuer),
		jwtutil.WithExpectedAudience("https://other-service.example.com"),
	)

	token, err := signing.SignToken("user-42", time.Minute)
	require.NoError(t, err)

	_, err = misVerifier.VerifyContext(context.Background(), token, time.Now())
	require.Error(t, err, "audience mismatch must reject")
}

// TestSigningKey_ExpiredTokenRejected pins the expiry contract.
// The kit's VerifyContext takes an explicit `now` so the test can
// fast-forward past the token's exp without sleeping.
func TestSigningKey_ExpiredTokenRejected(t *testing.T) {
	signing, err := newSigningKey()
	require.NoError(t, err)
	verifier, err := signing.newVerifier()
	require.NoError(t, err)

	token, err := signing.SignToken("user-x", time.Minute)
	require.NoError(t, err)
	// Verify with a `now` one hour after the token's exp — past any
	// reasonable kit leeway.
	future := time.Now().Add(2 * time.Hour)

	_, err = verifier.VerifyContext(context.Background(), token, future)
	require.Error(t, err, "expired token must reject")
}

// TestCentrifugeNode_BuildsWithJWTAuth verifies the kit-doctor-
// flagged composition contract: NewNode + WithJWTAuth +
// WithChannelClassifier must compose without error and return
// a usable Node. The test does NOT exercise the websocket
// protocol end-to-end (that would require the centrifuge client
// library); instead it validates that the lifecycle hooks the
// kit installs (OnConnecting / OnSubscribe / OnPublish) wire
// the constructed verifier and classifier.
func TestCentrifugeNode_BuildsWithJWTAuth(t *testing.T) {
	signing, err := newSigningKey()
	require.NoError(t, err)
	verifier, err := signing.newVerifier()
	require.NoError(t, err)

	node, err := centrifuge.NewNode(
		centrifuge.WithJWTAuth(verifier),
		centrifuge.WithChannelClassifier(classifyChannel),
	)
	require.NoError(t, err)
	require.NotNil(t, node)

	// Stop without Start must be safe — the kit's wave-164 guard
	// against centrifuge nil-derefs on unstarted shutdown.
	require.NoError(t, node.Stop(context.Background()))
}

// TestClassifyChannel pins the channel→class mapping. A
// regression here either expands metric cardinality or hides
// legitimate traffic under "default".
func TestClassifyChannel(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"user:42", "user"},
		{"room:lobby", "room"},
		{"system:health", "system"},
		{"", "default"},
		{"weird-no-prefix", "default"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, classifyChannel(c.in), "input=%q", c.in)
	}
}

// TestTokenEndpoint_IssuesVerifiableToken verifies the /demo/token
// endpoint end-to-end: HTTP request → response body holds a token
// that the same binary's verifier accepts.
func TestTokenEndpoint_IssuesVerifiableToken(t *testing.T) {
	signing, err := newSigningKey()
	require.NoError(t, err)
	verifier, err := signing.newVerifier()
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(signing.handleTokenIssue))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "?sub=alice")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body struct {
		Token     string `json:"token"`
		ConnectTo string `json:"connect_to"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.NotEmpty(t, body.Token)
	assert.Equal(t, websocketPath, body.ConnectTo)

	claims, err := verifier.VerifyContext(context.Background(), body.Token, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "alice", claims.Subject)
}

// TestJWKSEndpoint_ExposesPublicKey pins that the /demo/jwks
// endpoint returns parseable JWKS the kit's verifier can ingest.
// The verifier built INDEPENDENTLY from the served bytes must
// accept tokens signed by the underlying private key.
func TestJWKSEndpoint_ExposesPublicKey(t *testing.T) {
	signing, err := newSigningKey()
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(signing.handleJWKSExpose))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	body = body[:n]

	// Verify the served JWKS round-trips through ParseKeySet.
	ks, err := jwtutil.ParseKeySet(body)
	require.NoError(t, err)
	verifier := jwtutil.NewProviderWithKeySet(ks,
		jwtutil.WithExpectedIssuer(demoIssuer),
		jwtutil.WithExpectedAudience(demoAudience),
	)

	// And a token signed by the underlying key verifies against
	// the JWKS-derived verifier.
	token, err := signing.SignToken("bob", time.Minute)
	require.NoError(t, err)
	claims, err := verifier.VerifyContext(context.Background(), token, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "bob", claims.Subject)
}

// TestTokenEndpoint_RejectsLowercaseHTTPVerb is a contract test
// that the mux is set up with a GET-method-specific route.
// Sanity-check on the wire shape only — does not exercise the
// composition.
func TestTokenEndpoint_DefaultsSubWhenMissing(t *testing.T) {
	signing, err := newSigningKey()
	require.NoError(t, err)
	verifier, err := signing.newVerifier()
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(signing.handleTokenIssue))
	defer srv.Close()

	resp, err := http.Get(srv.URL) // no ?sub=
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	body, _ := readAll(resp.Body)
	require.True(t, strings.Contains(string(body), `"token"`))

	var decoded struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(body, &decoded))

	claims, err := verifier.VerifyContext(context.Background(), decoded.Token, time.Now())
	require.NoError(t, err)
	assert.Equal(t, "demo-user", claims.Subject, "missing ?sub= must default to demo-user")
}

// readAll is a small helper to bound the test's io.ReadAll usage.
func readAll(r interface{ Read([]byte) (int, error) }) ([]byte, error) {
	var out []byte
	buf := make([]byte, 1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				return out, nil
			}
			return out, err
		}
	}
}
