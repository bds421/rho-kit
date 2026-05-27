// Package github.com/bds421/rho-kit/auth/oauth2/v2 — OAuth2 / OIDC
// relying-party client. Separate module so services that only verify
// JWTs (via security/jwtutil) don't pull the OAuth2 + OIDC discovery
// surface.
//
// Dual to security/jwtutil: that package VERIFIES incoming JWTs;
// this package ISSUES login redirects, EXCHANGES auth codes, and
// REFRESHES tokens against an upstream OIDC issuer.
//
// Implemented on top of stdlib net/http + net/url + encoding/json
// (the OAuth2 flow is small enough that the golang.org/x/oauth2
// dependency would pull more than it earns). Callers wanting the
// golang.org/x/oauth2 surface (refresh-token transport wrappers, etc.)
// can construct one off the side using the discovered token endpoint
// from [Client.TokenEndpoint] — both worlds compose cleanly.
module github.com/bds421/rho-kit/auth/oauth2/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.0.0
	github.com/stretchr/testify v1.11.1
)

replace github.com/bds421/rho-kit/core/v2 => ../../core
