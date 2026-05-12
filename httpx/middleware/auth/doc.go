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
//     below).
//     The X-User-Id value must be a singleton identity token: no duplicate
//     header lines, comma-combined values, whitespace, or control characters.
//
// # Authorization
//
// RequirePermission, PermissionByMethod, and RequireScope all share a
// fail-closed contract:
//
//  1. If the request carries the trusted-S2S marker (set by the mTLS
//     branch of RequireS2SAuth), the check is bypassed.
//  2. Otherwise the relevant claim (permissions / scopes) on context must
//     satisfy the requirement.
//  3. Anything else (no claim, no marker, no auth middleware in front)
//     returns 403.
//
// The historic "no permissions claim ⇒ trusted ⇒ pass through" rule was a
// fail-open footgun: any misconfiguration that left the permissions claim
// absent (broken JWT issuer, missing auth middleware, route mounted on
// the wrong router) silently granted the caller full access. The marker
// makes trust explicit: it can only be set by the verified-mTLS path, and
// is the only thing that bypasses RBAC.
//
// RequireScopeStrict is even stricter — the marker does NOT bypass it,
// because the use case (force every caller to present a scope) is the
// reason a service operator picks Strict over the regular variant.
//
// # Test helpers
//
//   - WithUserID, WithPermissions: simulate the JWT path.
//   - WithTrustedS2S: simulate the mTLS path's marker. Available only
//     under the `authtest` build tag; in default builds it panics so an
//     accidental production import fails loudly instead of silently
//     bypassing RBAC.
//
// All three are clearly labelled "tests only" — using them in production
// code defeats the auth middleware.
package auth
