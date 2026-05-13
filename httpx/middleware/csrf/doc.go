// Package csrf provides cookie-based double-submit CSRF protection and
// strict content-type enforcement for HTTP handlers.
//
// This is the HTTP middleware layer. The wire-format primitives
// (HMAC-bound token generation and verification) live in the kit-level
// [github.com/bds421/rho-kit/security/v2/csrf] package; this package
// composes them with request/response handling, secure cookies, and
// SameSite defaults so handlers do not see the underlying secret.
//
// Key entry points:
//
//   - [Middleware] — install double-submit CSRF protection. Sets a
//     hardened cookie (HttpOnly false because JS must read it,
//     SameSite=Lax by default, Secure outside loopback), then on
//     mutating methods requires the `X-CSRF-Token` header to match
//     the cookie under an HMAC binding to the session.
//   - [RequireJSON] — content-type guard. Reject non-JSON request
//     bodies before they reach a handler that would otherwise parse
//     them defensively.
//   - [Option] / [WithCookieName] / [WithHeaderName] / [WithTTL] —
//     tune the cookie shape for legacy clients without rebuilding the
//     middleware.
//
// Token format and rotation rules are governed by the security/csrf
// package; consult its doc.go before changing defaults.
//
// # Key memory hygiene
//
// HMAC secrets passed to [WithSecret] / [WithSecrets] are wrapped in
// [secret.String] internally. Reveals on the hot path use
// [secret.String.Use] so plaintext key bytes live only for the
// duration of one HMAC compute — never as a long-lived []byte on the
// heap (Lens F A.7).
package csrf
