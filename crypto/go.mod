// The kit's crypto module bundles every primitive: encrypt (AEAD),
// envelope (envelope encryption + the kekstatic KEK adapter), masking,
// paseto, passhash, signing. v2 collapsed these from per-package
// modules into one because the dep cluster is consistent
// (golang.org/x/crypto + paseto + argon2) and they typically compose.
//
// Future heavy KEK adapters (cloud KMS implementations) live in their
// own modules — kekstatic stays inside this one because it is
// stdlib-only.
module github.com/bds421/rho-kit/crypto/v2

go 1.26.2

require (
	aidanwoods.dev/go-paseto v1.6.0
	github.com/stretchr/testify v1.11.1
	github.com/tink-crypto/tink-go/v2 v2.6.0
	golang.org/x/crypto v0.50.0
)

require (
	aidanwoods.dev/go-result v0.3.1 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/kr/pretty v0.3.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	golang.org/x/sys v0.43.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
