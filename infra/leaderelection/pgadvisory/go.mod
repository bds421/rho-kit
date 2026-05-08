module github.com/bds421/rho-kit/infra/leaderelection/pgadvisory/v2

go 1.26.2

require (
	github.com/bds421/rho-kit/data/v2 v2.0.0
	github.com/bds421/rho-kit/data/lock/pgadvisory/v2 v2.0.0
	github.com/bds421/rho-kit/infra/v2 v2.0.0
)

replace (
	github.com/bds421/rho-kit/data/lock/pgadvisory/v2 => ../../../data/lock/pgadvisory
	github.com/bds421/rho-kit/infra/v2/leaderelection => ../
)

replace github.com/bds421/rho-kit/data/v2 => ../../../data

replace github.com/bds421/rho-kit/infra/v2 => ../../../infra
