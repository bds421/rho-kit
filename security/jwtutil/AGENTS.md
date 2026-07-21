# AGENTS.md — `security/jwtutil`

## When to use this package

- Verifying JWTs issued by an external IdP (Auth0, Okta, Cognito, your own).
- Issuing JWTs for service-to-service (S2S) auth — the kit's `SigningProvider` handles key rotation.
- WebSocket / centrifuge / MCP handlers that need bearer-token verification at connect time.

## When to use something else

- **PASETO instead of JWT:** the kit ships `app/paseto` for the PASETO v4 token path. PASETO has fewer footguns; consider it for new services.
- **mTLS-based auth (no bearer tokens):** `security/mtlsidentity` + `grpcx/interceptor.MTLSAuthUnary`.

## Key APIs

- `Provider` — verifier with JWKS support and revocation check hook.
- `WithExpectedIssuer(iss)` / `WithExpectedAudience(aud)` — **MANDATORY for multi-service deployments** (Provider construction and raw `KeySet.Verify`). Without them, a token issued for service A is silently valid at service B if they share a signer (confused-deputy attack, RFC 7519 §4.1.3).
- `WithAllowAnyIssuer()` / `WithAllowAnyAudience()` (Provider options) and `KeySet.WithAllowAnyIssuer` / `KeySet.WithAllowAnyAudience` / `AllowAny*` fields — explicit override; "Any" makes the relaxation auditable in code review. `KeySet.Verify` returns `ErrPolicyRequired` when neither Expected* nor AllowAny* is set for a dimension.
- `WithRevocationChecker(checker)` — optional per-token revocation lookup (Redis JTI denylist, etc.).
- `Provider.VerifyContext(ctx, token, now)` — returns `*Claims` or error. Time argument is explicit for reproducible tests.

## Common mistakes

- **`ParseKeySet` on a JWKS that contains symmetric (HMAC) keys** — the kit rejects them to defeat the algorithm-confusion attack (a token signed with HS256 accepted by a verifier holding an RSA public key, treating the public-key bytes as the HMAC secret). Trusted JWKS endpoints don't publish symmetric keys.
- **No `WithExpectedAudience` / audience policy in a multi-service deployment** — see above. Always set the audience (or an explicit AllowAny* opt-out). Raw `KeySet.Verify` fails closed with `ErrPolicyRequired` when policy is missing.
- **Verifying without `WithRevocationChecker` on a long-lived token** — if your tokens have hour-long expiry, revocation lookup is the only way to log out a compromised user before the token expires.
- **Reading `claims.Permissions` / `claims.Scopes` as user input** — these are signed claims; trust them. But scope strings like `admin:write` are still string-typed; use `authz` package's typed scope parsing for safety.

## Observability

- Metrics: JWKS fetch outcomes via `jwks_fetch_failures_total{reason}` with bounded reason enum.
- No per-verify metrics by default; the failure rate is more useful at the HTTP middleware layer (`httpx/middleware/auth.JWT`).
