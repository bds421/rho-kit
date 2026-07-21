// Package github.com/bds421/rho-kit/data/saga/pgstore/v2 — Postgres
// StateStore for runtime/saga.DurableExecutor. Separate module so the
// in-memory saga executor consumers don't pull pgx.
module github.com/bds421/rho-kit/data/saga/pgstore/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.5.0
	github.com/bds421/rho-kit/runtime/v2 v2.5.0
	github.com/stretchr/testify v1.11.1
)

require (
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/kr/text v0.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
