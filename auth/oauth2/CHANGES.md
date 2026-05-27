# Changes

## Unreleased — v2.0

- Initial release.
- OAuth2 / OIDC relying-party client with PKCE on by default.
- Built on `golang.org/x/oauth2` (OAuth2 dance + PKCE helpers) and
  `github.com/coreos/go-oidc/v3` (OIDC discovery + ID-token
  signature/claims verification). Security-critical primitives come
  from audited libraries, not hand-rolled.
- Handlers for /login, /callback, /logout.
- SessionStore + StateStore interfaces with in-memory backends.
- ID-token verification covers signature + alg + audience + issuer +
  exp (via go-oidc) + nonce (kit double-check).
- `Client.OAuth2Config()` and `Client.Provider()` expose the
  underlying types for refresh-token transports and UserInfo.
