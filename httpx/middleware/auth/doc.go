// Package auth provides authentication and authorization middleware.
//
// # Authentication
//
//   - JWT: verifies a Bearer JWT issued by the kit's JWKS
//     provider. Rejects every other auth mode (X-User-Id headers, mTLS).
//   - RequireS2SAuth: accepts EITHER a Bearer JWT (same rules) OR a
//     verified mTLS client cert with an allow-listed CN paired with an
//     X-User-Id header approved by WithS2SImpersonationGuard. The mTLS branch
//     is the only path that stamps the trusted-S2S marker (see "Authorization"
//     below). Optional X-Permissions / X-Scopes headers are adopted so
//     user entitlements survive trusted hops (see [AppendOutgoingIdentity]).
//     The X-User-Id value must be a singleton identity token: no duplicate
//     header lines, comma-combined values, whitespace, or control characters.
//   - ChainMiddleware: tries multiple [Authenticator]s in order. When
//     combining session tokens, scoped API keys, and JWTs, register session
//     first, scoped keys second, JWT last — see [ChainMiddleware] for why.
//
// # Authorization
//
// RequirePermission, PermissionByMethod, and RequireScope all share a
// fail-closed contract:
//
//  1. The relevant claim (permissions / scopes) on context must satisfy
//     the requirement. Claims come from JWT verification or from trusted
//     S2S entitlement headers adopted on the mTLS path.
//  2. [IsTrustedS2S] alone does NOT bypass the check unless the middleware
//     is constructed with [WithTrustedS2SBypass] (service-level trust).
//  3. Anything else (no claim, no matching entitlement, no auth middleware
//     in front) returns 403.
//
// The historic "no permissions claim ⇒ trusted ⇒ pass through" rule was a
// fail-open footgun: any misconfiguration that left the permissions claim
// absent (broken JWT issuer, missing auth middleware, route mounted on
// the wrong router) silently granted the caller full access. The marker
// makes trust explicit for handlers and for opt-in bypass, but default
// Require* still enforces user entitlements across S2S hops — matching
// grpcx/interceptor.
//
// RequireScopeStrict is even stricter — it never honours
// WithTrustedS2SBypass, because the use case (force every caller to present
// a scope) is the reason a service operator picks Strict over the regular
// variant.
//
// # Test helpers
//
//   - WithUserID, WithPermissions: simulate the JWT path. Available only
//     under the `authtest` build tag.
//   - WithTrustedS2S: simulate the mTLS path's marker. Available only
//     under the `authtest` build tag.
//
// Default builds do not expose direct auth-context injection helpers; using
// them requires opting into `-tags authtest`.
package auth
