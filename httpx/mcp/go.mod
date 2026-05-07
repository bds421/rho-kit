module github.com/bds421/rho-kit/httpx/mcp

go 1.26.2

require (
	github.com/bds421/rho-kit/core/apperror v1.2.0
	github.com/bds421/rho-kit/core/tenant v0.0.0
	github.com/bds421/rho-kit/core/validate v0.0.0
	github.com/bds421/rho-kit/data/actionlog v0.0.0
	github.com/bds421/rho-kit/data/actionlog/memory v0.0.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/gabriel-vasile/mimetype v1.4.12 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.30.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/leodido/go-urn v1.4.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	golang.org/x/crypto v0.49.0 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/text v0.35.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace (
	github.com/bds421/rho-kit/core/apperror => ../../core/apperror
	github.com/bds421/rho-kit/core/tenant => ../../core/tenant
	github.com/bds421/rho-kit/core/validate => ../../core/validate
	github.com/bds421/rho-kit/data/actionlog => ../../data/actionlog
	github.com/bds421/rho-kit/data/actionlog/memory => ../../data/actionlog/memory
)
