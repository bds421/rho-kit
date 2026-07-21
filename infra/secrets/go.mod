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
	github.com/bds421/rho-kit/core/v2 v2.5.0
	github.com/prometheus/client_golang v1.23.2
	github.com/prometheus/client_model v0.6.2
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/prometheus/common v0.70.0 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	golang.org/x/sys v0.47.0 // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
