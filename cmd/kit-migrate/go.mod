module github.com/bds421/rho-kit/cmd/kit-migrate/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/data/actionlog/postgres/v2 v2.0.0
	github.com/bds421/rho-kit/data/approval/postgres/v2 v2.0.0
	github.com/bds421/rho-kit/data/idempotency/pgstore/v2 v2.0.0
)

require (
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	golang.org/x/sync v0.20.0 // indirect
)

require (
	github.com/bds421/rho-kit/data/v2 v2.0.0 // indirect
	github.com/jackc/pgx/v5 v5.9.2 // indirect
	golang.org/x/text v0.36.0 // indirect
)

replace github.com/bds421/rho-kit/data/v2 => ../../data

replace github.com/bds421/rho-kit/data/actionlog/postgres/v2 => ../../data/actionlog/postgres

replace github.com/bds421/rho-kit/data/approval/postgres/v2 => ../../data/approval/postgres

replace github.com/bds421/rho-kit/data/idempotency/pgstore/v2 => ../../data/idempotency/pgstore
