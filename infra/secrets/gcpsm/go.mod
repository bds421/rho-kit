// Package github.com/bds421/rho-kit/infra/secrets/gcpsm/v2 — GCP
// Secret Manager backend for infra/secrets.Loader. Separate module so
// the cloud.google.com/go SDK closure only lands in services that
// import it.
module github.com/bds421/rho-kit/infra/secrets/gcpsm/v2

go 1.26.2

require (
	cloud.google.com/go/secretmanager v1.14.7
	github.com/bds421/rho-kit/core/v2 v2.0.0
	github.com/bds421/rho-kit/infra/secrets/v2 v2.0.0
	github.com/stretchr/testify v1.11.1
	google.golang.org/grpc v1.81.0
)
