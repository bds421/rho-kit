# auth/oauth2

## Purpose

OAuth2 / OIDC relying-party client. Companion to `security/jwtutil`
(which verifies inbound JWTs); this package handles the upstream-
provider side: login redirects, code exchange, refresh, session.

## Built on standard libraries

- **`golang.org/x/oauth2`** drives the OAuth2 dance: AuthCodeURL,
  Exchange, PKCE helpers (`GenerateVerifier` + `S256ChallengeOption`),
  refresh-token transport.
- **`github.com/coreos/go-oidc/v3`** drives OIDC: provider discovery
  with issuer-match validation (RFC 8414 §3.3), JWKS fetch + rotation,
  ID-token verification (signature, alg, exp, audience, issuer).

The kit adds session/state persistence interfaces, sensible cookie
defaults, and the wrap-into-an-http.Handler ergonomics. Security-
critical primitives come from the audited libraries above, NOT
hand-rolled. `Client.OAuth2Config()` and `Client.Provider()` expose
the underlying types for callers needing refresh-token transports
or UserInfo without re-discovering endpoints.

## Public API

- `NewClient(ctx, cfg, opts...) (*Client, error)` — fetches well-known
  at construction, fails fast on bad config
- `Client.Handlers() http.Handler` — mount under a path prefix:
  `mux.Handle("/oauth/", client.Handlers())` exposes /login, /callback,
  /logout
- `SessionStore` / `StateStore` interfaces + in-memory backends:
  `NewMemorySessionStore()`, `NewMemoryStateStore()`
- Options: `WithSessionStore`, `WithStateStore`, `WithHTTPClient`,
  `WithLogger`, `WithSessionTTL`, `WithStateTTL`, `WithCookieName`,
  `WithCookieDomain`, `WithInsecureCookie`, `WithoutPKCE`

## Security defaults

- **PKCE on by default.** Public clients MUST use PKCE per RFC 7636;
  the default keeps confidential clients honest too. `WithoutPKCE()`
  is opt-out for providers that don't support it.
- **State + nonce CSRF guards.** Every login generates fresh state +
  nonce, persists them, and re-validates on callback. Single-use:
  state is deleted before code exchange so a replay can't succeed
  even on a slow exchange.
- **Cookie defaults:** HttpOnly, Secure, SameSite=Lax. Override Secure
  via `WithInsecureCookie()` only for local-dev over HTTP.
- **Issuer match.** Discovery rejects when the well-known doc's
  `issuer` field doesn't equal the configured Issuer (RFC 8414 §3.3).
- **Stdlib only.** Implemented on `net/http` + `net/url` +
  `encoding/json`; no `golang.org/x/oauth2` dep. Tiny closure.

## ID-token verification

The kit does **minimum** ID-token verification: split-on-dot, decode
payload, check nonce + sub. We do NOT verify signature, audience, or
issuer claim of the ID token by default — the token arrived over TLS
from the discovered token endpoint, so transport security covers
tamper-resistance. Defence-in-depth callers should pair this client
with `security/jwtutil.Provider` against the issuer's JWKS to verify
signature + alg-pinning before trusting claims.

## Tests

`go test -race ./...`. Covers:

- Happy-path NewClient against a fake OIDC issuer
- Missing-field rejections (no Issuer / ClientID / RedirectURL)
- Missing-store rejections (no SessionStore / StateStore)
- Issuer-mismatch in well-known doc rejected
- Login handler issues 302 with state + nonce + code_challenge=S256
- End-to-end browser flow: login → /authorize redirect → callback →
  session cookie set + persisted Session with userID + access token
- Unknown-state callback rejected with 400 "state mismatch"
- Logout clears cookie (MaxAge=-1)
- MemorySessionStore + MemoryStateStore TTL expiry

## See also

- `security/jwtutil` — inbound JWT verification. Pair with this
  client for defence-in-depth signature checks on the id_token.
- `httpx/middleware/auth` — pulls verified identity off ctx.
- `security/csrf` — pair with the callback handler if your app uses
  HTML forms (the OAuth2 dance itself is GET-only; CSRF guards apply
  to the application-side endpoints the session unlocks).
