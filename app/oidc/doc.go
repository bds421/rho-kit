// Package oidc composes browser OIDC login, callback, logout, and optional
// canonical-principal projection into an app.Builder module. It is deliberately
// separate from app/jwt: API resource services should use JWT verification
// without importing browser session or OIDC discovery dependencies.
//
// Routes other than /oauth/* receive the mapped identity.Principal and, via
// [SessionClaims] / [DecodeSessionClaims], the session's verified ID-token
// claims (including object and array claims). Token refresh and
// RP-initiated logout are enabled through [WithClientOption] with
// auth/oauth2.WithTokenRefresh and auth/oauth2.WithRPInitiatedLogout.
// identity.MappingProfile.AllowMissingClaims makes selected claims optional.
package oidc
