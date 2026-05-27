package oauth2

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/bds421/rho-kit/core/v2/secret"
)

// Config is the static configuration of an OAuth2/OIDC client.
type Config struct {
	// Issuer is the OIDC issuer URL (e.g. "https://login.example.com").
	// Required. The client fetches /.well-known/openid-configuration
	// from this URL to discover endpoints.
	Issuer string
	// ClientID is the client identifier registered with the issuer.
	// Required.
	ClientID string
	// ClientSecret is the confidential-client secret. May be nil for
	// public clients (mobile, SPA) — PKCE protects the flow when
	// absent. Wrapped in secret.String for zeroizable memory hygiene.
	ClientSecret *secret.String
	// RedirectURL is the absolute callback URL registered with the
	// issuer. Must match exactly (no path normalisation).
	RedirectURL string
	// Scopes requested from the issuer. "openid" is added
	// automatically; pass "profile", "email", or provider-specific
	// scopes as needed.
	Scopes []string
}

// Session is the post-authentication state stored per logged-in user.
type Session struct {
	// SessionID is the opaque identifier the session cookie carries.
	SessionID string
	// UserID is the subject claim ("sub") of the ID token.
	UserID string
	// AccessToken is the bearer token to call resource APIs. Wrapped
	// in secret.String so it's zeroed on session expiry.
	AccessToken *secret.String
	// RefreshToken is used to obtain new access tokens without a new
	// user login. Nil if the issuer didn't grant one. Also wrapped.
	RefreshToken *secret.String
	// Expiry is the access token's expiration time.
	Expiry time.Time
	// Claims is the verified ID-token claim set. Includes "sub",
	// "email", "name", and provider-specific claims.
	Claims map[string]any
}

// SessionStore is the kit's pluggable persistence for [Session].
// Implementations MUST be safe for concurrent use. Backends should
// honour TTL on Put (deleting expired entries) and zeroize stored
// secret bytes on Delete so credentials don't linger in memory.
type SessionStore interface {
	Put(ctx context.Context, sessionID string, sess Session, ttl time.Duration) error
	Get(ctx context.Context, sessionID string) (Session, error)
	Delete(ctx context.Context, sessionID string) error
}

// StateStore persists the per-login CSRF state token + OIDC nonce so
// the callback can validate the redirect matches a freshly-issued
// login (i.e. block a CSRF-driven callback from logging the user into
// the attacker's account).
type StateStore interface {
	Put(ctx context.Context, state string, entry StateEntry, ttl time.Duration) error
	Get(ctx context.Context, state string) (StateEntry, error)
	Delete(ctx context.Context, state string) error
}

// StateEntry is one in-flight login. Holds the OIDC nonce + the PKCE
// code verifier (which is needed at code-exchange time and MUST stay
// on the server — the client only sends the SHA-256 code_challenge).
type StateEntry struct {
	Nonce           string
	CodeVerifier    string
	RedirectTo      string // optional post-login deep link
	CreatedAt       time.Time
}

// Sentinel errors.
var (
	// ErrSessionNotFound is returned by SessionStore.Get when the ID
	// is unknown or expired.
	ErrSessionNotFound = errors.New("oauth2: session not found")
	// ErrStateNotFound is returned by StateStore.Get when the state
	// token doesn't match a recent login (replay or CSRF attempt).
	ErrStateNotFound = errors.New("oauth2: state not found")
	// ErrIssuerDiscovery wraps any failure to fetch / parse the
	// issuer's well-known OIDC discovery document.
	ErrIssuerDiscovery = errors.New("oauth2: issuer discovery failed")
	// ErrStateMismatch is returned by the callback handler when the
	// state in the query string doesn't match the one we issued.
	ErrStateMismatch = errors.New("oauth2: state mismatch")
	// ErrNonceMismatch is returned when the ID token's nonce claim
	// doesn't match the one we issued.
	ErrNonceMismatch = errors.New("oauth2: id_token nonce mismatch")
	// ErrCodeExchange wraps token-endpoint exchange failures.
	ErrCodeExchange = errors.New("oauth2: code exchange failed")
)

// Handlers is the interface returned by [Client.Handlers]. The login
// handler redirects to the issuer; the callback handler exchanges the
// code + sets the session cookie. Mounted under a path prefix:
//
//	mux.Handle("/oauth/", oauthClient.Handlers())
//
// exposes /oauth/login, /oauth/callback, /oauth/logout.
type Handlers interface {
	http.Handler
}
