// Package github.com/bds421/rho-kit/auth/oauth2/v2 — OAuth2 / OIDC
// relying-party client. Separate module so services that only verify
// JWTs (via security/jwtutil) don't pull the OAuth2 + OIDC discovery
// surface.
//
// Built on golang.org/x/oauth2 + github.com/coreos/go-oidc/v3 —
// battle-tested libraries that the broader Go ecosystem audits and
// patches. Rolling our own protocol code would trade a small dep
// surface for a large security-bug surface; the kit takes the proven
// path.
//
// Dual to security/jwtutil: that package VERIFIES incoming JWTs;
// this package ISSUES login redirects, EXCHANGES auth codes, and
// REFRESHES tokens against an upstream OIDC issuer.
module github.com/bds421/rho-kit/auth/oauth2/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.7.0
	github.com/bds421/rho-kit/observability/v2 v2.7.0
	github.com/coreos/go-oidc/v3 v3.21.0
	github.com/go-jose/go-jose/v4 v4.1.5
	github.com/prometheus/client_golang v1.24.1
	github.com/stretchr/testify v1.12.1
	golang.org/x/oauth2 v0.36.0
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/common v0.70.1 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sys v0.47.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
