// Package github.com/bds421/rho-kit/data/saga/pgstore/v2 — Postgres
// StateStore for runtime/saga.DurableExecutor. Separate module so the
// in-memory saga executor consumers don't pull pgx.
module github.com/bds421/rho-kit/data/saga/pgstore/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.0.0
	github.com/bds421/rho-kit/runtime/v2 v2.0.0
	github.com/stretchr/testify v1.11.1
)

replace github.com/bds421/rho-kit/core/v2 => ../../../core

replace github.com/bds421/rho-kit/runtime/v2 => ../../../runtime
