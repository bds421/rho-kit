// Package azurekeyvault implements [crypto/envelope.KEK] against Azure Key
// Vault / Managed HSM keys. v2 includes this adapter so Azure deployments can
// keep envelope KEKs in a managed service with audit and rotation.
//
// Heavy: pulls the official Azure Key Vault keys SDK. Stays in its own module
// so consumers using AWS KMS, GCP KMS, Vault Transit, or static test KEKs do
// not pull Azure deps.
module github.com/bds421/rho-kit/crypto/envelope/azurekeyvault/v2

go 1.27.4

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.24.0
	github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys v1.5.0
	github.com/bds421/rho-kit/core/v2 v2.49.0
	github.com/bds421/rho-kit/crypto/v2 v2.49.0
	github.com/prometheus/client_golang v1.25.0
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/azidentity v1.15.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.13.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/internal v1.2.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pandatix/go-abnf v0.5.5 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/common v0.74.0 // indirect
	github.com/prometheus/procfs v0.23.0 // indirect
	github.com/tink-crypto/tink-go/v2 v2.9.0 // indirect
	golang.org/x/crypto v0.59.0 // indirect
	golang.org/x/net v0.63.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/text v0.44.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)
