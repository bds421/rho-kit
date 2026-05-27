# Changes

## Unreleased — v2.0

- Initial release.
- OAuth2 / OIDC relying-party client with PKCE-by-default.
- OIDC well-known discovery with issuer-match validation.
- Handlers for /login, /callback, /logout.
- SessionStore + StateStore interfaces with in-memory backends.
- ID-token minimum verification (nonce + sub); deeper signature
  validation deferred to security/jwtutil (caller-side).
- Stdlib-only implementation (no golang.org/x/oauth2 dep).
