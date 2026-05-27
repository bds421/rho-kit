// Package github.com/bds421/rho-kit/infra/secrets/awssm/v2 — AWS
// Secrets Manager backend for infra/secrets.Loader. Separate module so
// the aws-sdk-go-v2 dep closure only lands in services that import it.
module github.com/bds421/rho-kit/infra/secrets/awssm/v2

go 1.26.2

require (
	github.com/aws/aws-sdk-go-v2/service/secretsmanager v1.41.0
	github.com/bds421/rho-kit/core/v2 v2.0.0
	github.com/bds421/rho-kit/infra/secrets/v2 v2.0.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/aws/aws-sdk-go-v2 v1.32.0
	github.com/aws/smithy-go v1.22.0 // indirect
)

// Local-dev replaces. Mirrors the kit's existing split-module pattern
// (see crypto/envelope/awskms/go.mod). Stripped at release time by the
// FORBID_INTERNAL_REPLACES=1 make check-publishable gate before tagging.
replace github.com/bds421/rho-kit/infra/secrets/v2 => ../

replace github.com/bds421/rho-kit/core/v2 => ../../../core
