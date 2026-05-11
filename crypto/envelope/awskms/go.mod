// Package awskms implements [crypto/envelope.KEK] against AWS KMS
// using GenerateDataKey / Encrypt / Decrypt. v2 added cloud KMS
// adapters because the kekstatic adapter (in-process key) doesn't
// satisfy ASVS V6.4.1 in production — keys must live in a managed
// KMS with audit + rotation.
//
// Heavy: pulls aws-sdk-go-v2 + KMS service. Stays in its own module
// so consumers using GCP KMS or Vault Transit don't pull AWS deps.
module github.com/bds421/rho-kit/crypto/envelope/awskms/v2

go 1.26.2

require (
	github.com/aws/aws-sdk-go-v2 v1.41.2
	github.com/aws/aws-sdk-go-v2/service/kms v1.45.6
	github.com/bds421/rho-kit/crypto/v2 v2.0.0
)

require (
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.18 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.18 // indirect
	github.com/aws/smithy-go v1.24.1 // indirect
	github.com/tink-crypto/tink-go/v2 v2.6.0 // indirect
	golang.org/x/crypto v0.50.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

replace github.com/bds421/rho-kit/crypto/v2 => ../../
