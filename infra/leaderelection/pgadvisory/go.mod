module github.com/bds421/rho-kit/infra/leaderelection/pgadvisory

go 1.26.2

require (
	github.com/bds421/rho-kit/data/lock v1.1.0
	github.com/bds421/rho-kit/data/lock/pgadvisory v0.0.0
	github.com/bds421/rho-kit/infra/leaderelection v0.0.0
)

require github.com/fsnotify/fsnotify v1.10.1 // indirect

replace (
	github.com/bds421/rho-kit/data/lock/pgadvisory => ../../../data/lock/pgadvisory
	github.com/bds421/rho-kit/infra/leaderelection => ../
)
