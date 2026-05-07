module github.com/bds421/rho-kit/httpx/middleware/budget

go 1.26.2

require (
	github.com/bds421/rho-kit/core/tenant v0.0.0
	github.com/bds421/rho-kit/data/budget v0.0.0
	github.com/stretchr/testify v1.11.1
)

replace (
	github.com/bds421/rho-kit/core/tenant => ../../../core/tenant
	github.com/bds421/rho-kit/data/budget => ../../../data/budget
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
