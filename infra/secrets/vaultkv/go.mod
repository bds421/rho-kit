// Package github.com/bds421/rho-kit/infra/secrets/vaultkv/v2 — HashiCorp
// Vault KV v2 backend for infra/secrets.Loader. Separate module so the
// hashicorp/vault SDK closure only lands in services that import it.
module github.com/bds421/rho-kit/infra/secrets/vaultkv/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.0.0
	github.com/bds421/rho-kit/infra/secrets/v2 v2.0.0
	github.com/hashicorp/vault/api v1.16.0
	github.com/stretchr/testify v1.11.1
)
