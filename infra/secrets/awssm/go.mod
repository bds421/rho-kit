// Package github.com/bds421/rho-kit/infra/secrets/awssm/v2 — AWS
// Secrets Manager backend for infra/secrets.Loader. Separate module so
// the aws-sdk-go-v2 dep closure only lands in services that import it.
module github.com/bds421/rho-kit/infra/secrets/awssm/v2

go 1.26.8

require (
	github.com/aws/aws-sdk-go-v2/service/secretsmanager v1.50.6
	github.com/bds421/rho-kit/core/v2 v2.31.1
	github.com/bds421/rho-kit/infra/secrets/v2 v2.31.1
	github.com/stretchr/testify v1.12.1
)

require (
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.5.7 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.8.7 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_golang v1.25.0 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/common v0.72.1 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

require (
	github.com/aws/aws-sdk-go-v2 v1.47.4 // indirect
	github.com/aws/smithy-go v1.28.6 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)
