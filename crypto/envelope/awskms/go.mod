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
	github.com/aws/aws-sdk-go-v2 v1.41.9
	github.com/aws/aws-sdk-go-v2/service/kms v1.52.2
	github.com/aws/smithy-go v1.26.0
	github.com/bds421/rho-kit/core/v2 v2.0.2
	github.com/bds421/rho-kit/crypto/v2 v2.0.2
	github.com/prometheus/client_golang v1.23.2
)

require (
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.25 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.25 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.67.5 // indirect
	github.com/prometheus/procfs v0.20.1 // indirect
	github.com/tink-crypto/tink-go/v2 v2.6.0 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	golang.org/x/crypto v0.52.0 // indirect
	golang.org/x/sys v0.45.0 // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
)
