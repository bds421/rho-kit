// Package apikey is the HTTP middleware that authenticates requests bearing
// an opaque API key issued by [github.com/bds421/rho-kit/security/v2/apikey].
//
// [Middleware] extracts the token from the Authorization: Bearer header (or
// the X-API-Key header), looks the key up by its public id, verifies the
// secret in constant time, and — on success — attaches the key's id, owner,
// and scopes to the request context. Authentication failures are returned as
// 401 responses; the middleware never reveals whether a key id exists.
//
// [RequireScopes] enforces that the authenticated key carries the required
// scopes, returning 403 otherwise. Required scopes are validated against the
// shared authz registry at construction time so a typo fails fast at startup
// rather than silently never matching.
//
// This package lives inside the httpx module because it depends only on
// internal kit packages (the apikey core, authz, problemdetails) — no heavy
// SDK — so it adds no new dependency boundary. The Postgres-backed
// [apikey.Repository] it consumes lives in the data/apikey/postgres module.
package apikey
