module github.com/bds421/rho-kit/data/idempotency/pgstore/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/core/v2 v2.0.0
	github.com/bds421/rho-kit/data/v2 v2.0.0
)

replace github.com/bds421/rho-kit/core/v2 => ../../../core

replace github.com/bds421/rho-kit/data/v2 => ../../../data
