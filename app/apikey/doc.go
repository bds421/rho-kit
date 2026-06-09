// Package apikey is the lazy app-module wrapper for the API-key
// authentication middleware in
// [github.com/bds421/rho-kit/httpx/v2/middleware/apikey].
//
// Services that authenticate external/customer traffic with opaque API keys
// pass [Module] to [app.Builder.With]; services that don't, do not import
// this package. The module contributes its middleware at [app.PhaseAuth], so
// requests are authenticated alongside (and in the same phase as) JWT/PASETO
// credentials.
//
// Key issuance, rotation, and revocation live in the
// [github.com/bds421/rho-kit/security/v2/apikey] Manager — the privileged
// side is deliberately kept out of the request-path module.
package apikey
