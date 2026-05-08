module github.com/bds421/rho-kit/cmd/kit-migrate/v2

go 1.26.2

require github.com/bds421/rho-kit/data/idempotency/pgstore/v2 v2.0.0

require (
	github.com/bds421/rho-kit/data/v2 v2.0.0
	github.com/jackc/pgx/v5 v5.9.2 // indirect
	golang.org/x/text v0.36.0 // indirect
)

replace github.com/bds421/rho-kit/data/v2 => ../../data

replace github.com/bds421/rho-kit/data/idempotency/pgstore/v2 => ../../data/idempotency/pgstore
