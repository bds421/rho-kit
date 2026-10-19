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
  `WithCookieDomain`, `WithInsecureCookie`, `WithoutPKCE`,
  `WithTokenRefresh(leeway)` (transparent refresh in `SessionFromRequest`),
  `WithRPInitiatedLogout(postLogoutURI)` (logout answers 200 with a
  `LogoutResponse{LogoutURL}` instead of 204)
- `WithSignInHook(fn)`: runs after ID-token verification, before the session
  is stored; returns `Session.Attributes` (kept across refresh). Wrap
  `ErrSignInRefused` to refuse (403); other errors answer 502; either way no
  session is created
- `Session.IDToken` keeps the raw verified ID token for `id_token_hint`;
  `auth/oauth2/redis.WithEncryption` encrypts stored sessions/state
- `SessionUpdater` (optional store interface, `Update(ctx, id, expectedVersion,
  sess, ttl) (bool, error)`): compare-and-set used by token refresh so a
  logout during an in-flight refresh is never undone. Memory and Redis stores
  implement it; other stores fall back to re-check + Put (narrower race)
- Session lifetime: `WithAbsoluteSessionLifetime(d)` (cap at
  `Session.CreatedAt+d`; refresh/TTL never extend; sessions without
  CreatedAt fail closed), `WithIdleTimeout(d)` (`Session.LastActivity`,
  written at most once per minute via `SessionUpdater` only; `NewClient`
  fails without one). Background requests are marked with
  `WithBackgroundRequest(ctx)` or the `X-Background-Request: 1` header
  (`WithBackgroundRequestHeader(name)`); they may refresh but never extend
  the idle window
- `WithRefreshOutageGrace(d)`: transport/5xx refresh failures keep the
  session up to `Expiry+d`; 4xx ends it immediately
- `RefreshLocker` (optional store interface,
  `TryLockRefresh(ctx, id, ttl) (release, acquired, err)`): cross-replica
  refresh serialization; waiting replicas re-read and reuse the other
  replica's tokens up to `WithRefreshLockWait(d)` (default 5s). Redis
  implements it (SET NX PX + token-checked release); memory does not need it
- Back-channel logout: `WithBackChannelLogout()` adds
  `POST /oauth/backchannel-logout` (OIDC Back-Channel Logout 1.0: go-oidc
  verifier for signature/iss/aud/exp, iat within 5 min (+1 min skew), jti
  replay via `ReplayCache`, events claim, sid and/or sub, no nonce).
  Requires a `SessionIndex` store (`SessionIDsBySubject`,
  `SessionIDsBySID`; `Session.SID` from the ID token). Replay cache:
  `WithReplayCache`, else the session store if it implements `ReplayCache`
  (redis), else `NewMemoryReplayCache()`.
  `Client.DeleteSessionsBySubject(ctx, sub)` for app use;
  `WithSessionRevocationHook(fn)` runs once per affected subject
- Step-up: `Session.AMR`, `Session.AuthTime`; `GET /oauth/login` accepts
  allowlisted `prompt` (none|login|consent|select_account) and `max_age`
  (0..1y); with max_age the callback rejects an auth_time older than the
  login request minus max_age (`ErrStaleAuthentication`)

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
- **Audited libraries.** Built on `golang.org/x/oauth2` (OAuth2 dance +
  PKCE helpers) and `github.com/coreos/go-oidc/v3` (OIDC discovery +
  ID-token verification). Security-critical primitives are not
  hand-rolled.

## ID-token verification

The kit performs **full** ID-token verification via go-oidc. At
construction it builds a verifier — `provider.Verifier(&oidc.Config{
ClientID: cfg.ClientID})` — and the callback handler calls
`verifier.Verify()` on the returned `id_token`, which validates the
signature, signing alg, `exp`, audience (`aud` == ClientID), and
issuer (`iss`). The kit then double-checks the OIDC `nonce` against the
value persisted at login. No transport-only trust shortcut is taken.

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
- Absolute lifetime cap (incl. refresh TTL cap), idle timeout with
  header/context background markers, activity write throttling, logout
  between read and activity write stays deleted
- Outage grace boundaries (5xx, transport error, 4xx)
- Back-channel logout token validation (valid, wrong aud/iss, bad
  signature, replayed jti, events, nonce, iat window, sid vs sub),
  `DeleteSessionsBySubject`, memory index consistency
- Step-up parameter validation, max_age/auth_time enforcement, amr/auth_time
  exposure; redis lock contention and index tests live in auth/oauth2/redis

## See also

- `security/jwtutil` — inbound JWT verification. Pair with this
  client for defence-in-depth signature checks on the id_token.
- `httpx/middleware/auth` — pulls verified identity off ctx.
- `security/csrf` — pair with the callback handler if your app uses
  HTML forms (the OAuth2 dance itself is GET-only; CSRF guards apply
  to the application-side endpoints the session unlocks).
