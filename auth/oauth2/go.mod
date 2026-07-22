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
	github.com/bds421/rho-kit/core/v2 v2.6.0
	github.com/bds421/rho-kit/observability/v2 v2.6.0
	github.com/coreos/go-oidc/v3 v3.20.0
	github.com/go-jose/go-jose/v4 v4.1.4
	github.com/prometheus/client_golang v1.24.0
	github.com/stretchr/testify v1.11.1
	golang.org/x/oauth2 v0.36.0
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.70.0 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	golang.org/x/sys v0.47.0 // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
