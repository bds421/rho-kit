// Package github.com/bds421/rho-kit/infra/secrets/v2 is the kit's
// pluggable secret-loader umbrella. The Loader interface, the
// TTL-cached wrapper, and the rotating credential-provider live here
// without pulling any cloud SDK. Each backend (AWS Secrets Manager,
// GCP Secret Manager, Vault KV) is its own go-module so consumers pay
// only for what they import.
//
// Distinct from crypto/envelope/*: those wrap data-encryption keys
// (KEKs); this package loads secrets-as-values (database passwords,
// API tokens, signing keys) directly.
module github.com/bds421/rho-kit/infra/secrets/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.0.0
	github.com/prometheus/client_golang v1.23.2
	github.com/stretchr/testify v1.11.1
)
