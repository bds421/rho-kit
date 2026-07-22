// Package oidc composes browser OIDC login, callback, logout, and optional
// canonical-principal projection into an app.Builder module. It is deliberately
// separate from app/jwt: API resource services should use JWT verification
// without importing browser session or OIDC discovery dependencies.
package oidc
