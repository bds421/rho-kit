module github.com/bds421/rho-kit/data/lock/pgadvisory/v2

go 1.26.2

require github.com/stretchr/testify v1.11.1

require (
	github.com/kr/pretty v0.3.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
)

require (
	github.com/bds421/rho-kit/data/v2 v2.0.0
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/bds421/rho-kit/core/v2 => ../../../core

replace github.com/bds421/rho-kit/observability/v2 => ../../../observability

replace github.com/bds421/rho-kit/data/v2 => ../../../data

replace github.com/bds421/rho-kit/infra/v2 => ../../../infra
