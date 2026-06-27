// Package github.com/bds421/rho-kit/infra/secrets/awssm/v2 — AWS
// Secrets Manager backend for infra/secrets.Loader. Separate module so
// the aws-sdk-go-v2 dep closure only lands in services that import it.
module github.com/bds421/rho-kit/infra/secrets/awssm/v2

go 1.26.2

require (
	github.com/aws/aws-sdk-go-v2/service/secretsmanager v1.42.3
	github.com/bds421/rho-kit/core/v2 v2.2.0
	github.com/bds421/rho-kit/infra/secrets/v2 v2.2.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/aws/aws-sdk-go-v2/internal/configsources v1.4.29 // indirect
	github.com/aws/aws-sdk-go-v2/internal/endpoints/v2 v2.7.29 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/prometheus/client_golang v1.23.2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.67.5 // indirect
	github.com/prometheus/procfs v0.20.1 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	golang.org/x/sys v0.46.0 // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

require (
	github.com/aws/aws-sdk-go-v2 v1.42.0 // indirect
	github.com/aws/smithy-go v1.27.2 // indirect
)
