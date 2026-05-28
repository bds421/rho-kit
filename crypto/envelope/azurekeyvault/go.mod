// Package azurekeyvault implements [crypto/envelope.KEK] against Azure Key
// Vault / Managed HSM keys. v2 includes this adapter so Azure deployments can
// keep envelope KEKs in a managed service with audit and rotation.
//
// Heavy: pulls the official Azure Key Vault keys SDK. Stays in its own module
// so consumers using AWS KMS, GCP KMS, Vault Transit, or static test KEKs do
// not pull Azure deps.
module github.com/bds421/rho-kit/crypto/envelope/azurekeyvault/v2

go 1.26.2

require (
	github.com/Azure/azure-sdk-for-go/sdk/azcore v1.21.1
	github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys v1.4.0
	github.com/bds421/rho-kit/core/v2 v2.0.0
	github.com/bds421/rho-kit/crypto/v2 v2.0.0
)

require (
	github.com/Azure/azure-sdk-for-go/sdk/internal v1.12.0 // indirect
	github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/internal v1.2.0 // indirect
	github.com/tink-crypto/tink-go/v2 v2.6.0 // indirect
	golang.org/x/crypto v0.51.0 // indirect
	golang.org/x/net v0.54.0 // indirect
	golang.org/x/sys v0.44.0 // indirect
	golang.org/x/text v0.37.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)
