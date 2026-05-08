// The kit's security module bundles cross-cutting security
// primitives: csrf (CSRF token generation/validation), jwtutil
// (JWKS-backed JWT verification with alg-confusion mitigation),
// netutil (TLS config + IP allowlists). v2 collapsed these from
// per-package modules; the dep cluster (jwx + x/crypto) is shared
// across them. See AGENTS.md "Module shape".
module github.com/bds421/rho-kit/security

go 1.26

require (
	github.com/bds421/rho-kit/core v0.0.0
	github.com/bds421/rho-kit/resilience v0.0.0
	github.com/lestrrat-go/jwx/v3 v3.0.13
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/decred/dcrd/dcrec/secp256k1/v4 v4.4.0 // indirect
	github.com/fsnotify/fsnotify v1.10.1 // indirect
	github.com/goccy/go-json v0.10.3 // indirect
	github.com/lestrrat-go/blackmagic v1.0.4 // indirect
	github.com/lestrrat-go/dsig v1.0.0 // indirect
	github.com/lestrrat-go/dsig-secp256k1 v1.0.0 // indirect
	github.com/lestrrat-go/httpcc v1.0.1 // indirect
	github.com/lestrrat-go/httprc/v3 v3.0.2 // indirect
	github.com/lestrrat-go/option/v2 v2.0.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/valyala/fastjson v1.6.7 // indirect
	golang.org/x/crypto v0.50.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/bds421/rho-kit/core => ../core

replace github.com/bds421/rho-kit/resilience => ../resilience
