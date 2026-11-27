// Package oauth2test is a local OpenID Connect provider for tests of
// relying parties: discovery, JWKS, an authorization endpoint that signs the
// configured person in at once, and a token endpoint that checks the client,
// the redirect URI and the PKCE verifier and returns an RS256 ID token. It
// needs no network beyond loopback and holds no real accounts.
package oauth2test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

const keyID = "oauth2test-1"

// Issuer is a running test provider. Its fields may be changed between
// sign-ins; they are read under its lock.
type Issuer struct {
	server *httptest.Server
	key    *rsa.PrivateKey

	mu           sync.Mutex
	clientID     string
	clientSecret string
	redirects    []string
	claims       map[string]any
	denied       bool
	issParameter bool
	codes        map[string]grant
}

type grant struct {
	nonce       string
	challenge   string
	redirectURI string
	claims      map[string]any
}

// NewIssuer starts a provider for the client clientID with the secret
// clientSecret (empty for a public client) and the registered redirect URIs
// on a loopback httptest TLS server, closed when the test ends, so its
// issuer is an https URL as OpenID Connect requires; [Issuer.Client]
// trusts its certificate. A redirect URI must match one registered exactly. It signs in subject "user-1" with the address
// user@example.com, verified, until [Issuer.SetClaims] says otherwise.
func NewIssuer(t testing.TB, clientID, clientSecret string, redirectURIs ...string) *Issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("oauth2test: generate key: %v", err)
	}
	iss := &Issuer{
		key: key, clientID: clientID, clientSecret: clientSecret, redirects: redirectURIs,
		claims: map[string]any{"sub": "user-1", "email": "user@example.com", "email_verified": true, "name": "Test User"},
		codes:  make(map[string]grant),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", iss.discovery)
	mux.HandleFunc("GET /jwks", iss.jwks)
	mux.HandleFunc("GET /authorize", iss.authorize)
	mux.HandleFunc("POST /token", iss.token)
	iss.server = httptest.NewTLSServer(mux)
	t.Cleanup(iss.server.Close)
	return iss
}

// URL is the issuer identifier and base URL.
func (i *Issuer) URL() string { return i.server.URL }

// Client is an http.Client that reaches the issuer and trusts its
// certificate.
func (i *Issuer) Client() *http.Client { return i.server.Client() }

// SetClaims replaces the claims of the ID tokens issued from now on (sub,
// email, email_verified, name and any other). iss, aud, exp, iat and nonce
// are always set by the issuer.
func (i *Issuer) SetClaims(claims map[string]any) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.claims = maps.Clone(claims)
}

// SendIssuer makes the provider announce and send the iss parameter of
// RFC 9207 on its authorization responses.
func (i *Issuer) SendIssuer(send bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.issParameter = send
}

// Deny makes the authorization endpoint answer access_denied, as when the
// person cancels at the provider.
func (i *Issuer) Deny(denied bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.denied = denied
}

func (i *Issuer) discovery(w http.ResponseWriter, _ *http.Request) {
	base := i.server.URL
	i.mu.Lock()
	issParameter := i.issParameter
	i.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         base,
		"authorization_endpoint":                         base + "/authorize",
		"token_endpoint":                                 base + "/token",
		"jwks_uri":                                       base + "/jwks",
		"id_token_signing_alg_values_supported":          []string{"RS256"},
		"response_types_supported":                       []string{"code"},
		"subject_types_supported":                        []string{"public"},
		"code_challenge_methods_supported":               []string{"S256"},
		"authorization_response_iss_parameter_supported": issParameter,
	})
}

func (i *Issuer) jwks(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
		Key: &i.key.PublicKey, KeyID: keyID, Algorithm: string(jose.RS256), Use: "sig",
	}}})
}

// authorize signs the configured person in and redirects back with a code.
func (i *Issuer) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	registered := slices.Index(i.redirects, q.Get("redirect_uri"))
	if registered < 0 {
		http.Error(w, "unregistered redirect_uri", http.StatusBadRequest)
		return
	}
	back, err := url.Parse(i.redirects[registered])
	if err != nil || q.Get("client_id") != i.clientID || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	i.mu.Lock()
	denied, issParameter := i.denied, i.issParameter
	code := rand.Text()
	if !denied {
		i.codes[code] = grant{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirectURI: back.String(), claims: maps.Clone(i.claims)}
	}
	i.mu.Unlock()
	bq := back.Query()
	bq.Set("state", q.Get("state"))
	if issParameter {
		bq.Set("iss", i.server.URL)
	}
	if denied {
		bq.Set("error", "access_denied")
	} else {
		bq.Set("code", code)
	}
	back.RawQuery = bq.Encode()
	http.Redirect(w, r, back.String(), http.StatusFound)
}

// token redeems a code once, for the right client, redirect URI and verifier.
func (i *Issuer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if !i.clientAuthenticated(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
		return
	}
	i.mu.Lock()
	g, ok := i.codes[r.PostFormValue("code")]
	delete(i.codes, r.PostFormValue("code"))
	i.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
	if !ok || g.redirectURI != r.PostFormValue("redirect_uri") || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	idToken, err := i.sign(g)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": rand.Text(), "token_type": "Bearer", "expires_in": 300, "id_token": idToken,
	})
}

func (i *Issuer) clientAuthenticated(r *http.Request) bool {
	id, secret, basic := r.BasicAuth()
	if !basic {
		id, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
	}
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return id == i.clientID && subtle.ConstantTimeCompare([]byte(secret), []byte(i.clientSecret)) == 1
}

func (i *Issuer) sign(g grant) (string, error) {
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: i.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", keyID),
	)
	if err != nil {
		return "", err
	}
	now := time.Now()
	claims := maps.Clone(g.claims)
	maps.Copy(claims, map[string]any{
		"iss": i.server.URL, "aud": i.clientID, "nonce": g.nonce,
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(), "auth_time": now.Unix(),
	})
	return jwt.Signed(signer).Claims(claims).Serialize()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// LogoutEvent is the events member of a back-channel logout token.
const LogoutEvent = "http://schemas.openid.net/event/backchannel-logout"

// LogoutToken signs a back-channel logout token (OpenID Connect
// Back-Channel Logout 1.0) as the provider would post it: iss, aud, iat,
// exp, a fresh jti and the logout event, then claims (sub and/or sid; a
// nil value removes a claim, so tests can build invalid tokens).
func (i *Issuer) LogoutToken(t testing.TB, claims map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: i.key},
		(&jose.SignerOptions{}).WithType("logout+jwt").WithHeader("kid", keyID),
	)
	if err != nil {
		t.Fatalf("oauth2test: signer: %v", err)
	}
	now := time.Now()
	i.mu.Lock()
	all := map[string]any{
		"iss": i.server.URL, "aud": i.clientID, "iat": now.Unix(), "exp": now.Add(2 * time.Minute).Unix(),
		"jti": rand.Text(), "events": map[string]any{LogoutEvent: map[string]any{}},
	}
	i.mu.Unlock()
	for k, v := range claims {
		if v == nil {
			delete(all, k)
			continue
		}
		all[k] = v
	}
	raw, err := jwt.Signed(signer).Claims(all).Serialize()
	if err != nil {
		t.Fatalf("oauth2test: sign the logout token: %v", err)
	}
	return raw
}
