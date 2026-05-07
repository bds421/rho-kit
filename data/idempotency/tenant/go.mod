module github.com/bds421/rho-kit/data/idempotency/tenant

go 1.26.2

require (
	github.com/bds421/rho-kit/core/tenant v0.0.0
	github.com/bds421/rho-kit/data/idempotency v0.0.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/bds421/rho-kit/core/tenant => ../../../core/tenant
	github.com/bds421/rho-kit/data/idempotency => ../
)
