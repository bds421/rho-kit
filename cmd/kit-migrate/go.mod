module github.com/bds421/rho-kit/cmd/kit-migrate

go 1.26.2

require github.com/bds421/rho-kit/data/idempotency/pgstore v0.0.0

require (
	github.com/bds421/rho-kit/data v0.0.0
	github.com/jackc/pgx/v5 v5.9.2 // indirect
	golang.org/x/text v0.36.0 // indirect
)

replace github.com/bds421/rho-kit/data => ../../data

replace github.com/bds421/rho-kit/data/idempotency/pgstore => ../../data/idempotency/pgstore
