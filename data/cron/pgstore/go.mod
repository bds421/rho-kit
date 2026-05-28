// Package github.com/bds421/rho-kit/data/cron/pgstore/v2 — Postgres-
// backed persistent schedule store for runtime/cron. Separate module
// so consumers using the in-memory runtime/cron.Scheduler don't pull
// pgx.
module github.com/bds421/rho-kit/data/cron/pgstore/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.0.0
	github.com/bds421/rho-kit/runtime/v2 v2.0.0
	github.com/stretchr/testify v1.11.1
)
