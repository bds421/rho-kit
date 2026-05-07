module github.com/bds421/rho-kit/httpx/middleware/approval

go 1.26.2

require (
	github.com/bds421/rho-kit/data/approval v0.0.0
	github.com/bds421/rho-kit/data/approval/memory v0.0.0
	github.com/google/uuid v1.6.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/bds421/rho-kit/data/approval => ../../../data/approval

replace github.com/bds421/rho-kit/data/approval/memory => ../../../data/approval/memory
