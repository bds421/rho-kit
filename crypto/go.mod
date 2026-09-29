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

go 1.26.8

require (
	aidanwoods.dev/go-paseto v1.6.0
	github.com/bds421/rho-kit/core/v2 v2.8.0
	github.com/stretchr/testify v1.12.1
	github.com/tink-crypto/tink-go/v2 v2.8.0
	golang.org/x/crypto v0.57.0
)

require (
	aidanwoods.dev/go-result v0.3.1 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sys v0.48.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
