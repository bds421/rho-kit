module github.com/bds421/rho-kit/infra/messaging/natsbackend/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.0.0
	github.com/nats-io/nats.go v1.51.0
	github.com/stretchr/testify v1.11.1
)

require github.com/kr/text v0.2.0 // indirect

require (
	github.com/bds421/rho-kit/infra/v2 v2.0.0
	github.com/bds421/rho-kit/io/v2 v2.0.0 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.18.5 // indirect
	github.com/nats-io/nkeys v0.4.15 // indirect
	github.com/nats-io/nuid v1.0.1 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	golang.org/x/crypto v0.50.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/bds421/rho-kit/core/v2 => ../../../core

replace github.com/bds421/rho-kit/infra/v2 => ../../../infra

replace github.com/bds421/rho-kit/io/v2 => ../../../io
