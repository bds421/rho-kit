module github.com/bds421/rho-kit/infra/leaderelection/pgadvisory

go 1.26.2

require (
	github.com/bds421/rho-kit/data v0.0.0
	github.com/bds421/rho-kit/data/lock/pgadvisory v0.0.0
	github.com/bds421/rho-kit/infra v0.0.0
)

replace (
	github.com/bds421/rho-kit/data/lock/pgadvisory => ../../../data/lock/pgadvisory
	github.com/bds421/rho-kit/infra/leaderelection => ../
)

replace github.com/bds421/rho-kit/data => ../../../data

replace github.com/bds421/rho-kit/infra => ../../../infra
