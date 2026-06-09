// Package github.com/bds421/rho-kit/data/saga/pgstore/v2 — Postgres
// StateStore for runtime/saga.DurableExecutor. Separate module so the
// in-memory saga executor consumers don't pull pgx.
module github.com/bds421/rho-kit/data/saga/pgstore/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.1.0
	github.com/bds421/rho-kit/runtime/v2 v2.1.0
)

require github.com/google/uuid v1.6.0 // indirect
