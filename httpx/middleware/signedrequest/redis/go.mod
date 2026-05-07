module github.com/bds421/rho-kit/httpx/middleware/signedrequest/redis

go 1.26.2

require (
	github.com/bds421/rho-kit/httpx/middleware/signedrequest v0.0.0-00010101000000-000000000000
	github.com/redis/go-redis/v9 v9.18.0
)

replace github.com/bds421/rho-kit/httpx/middleware/signedrequest => ../

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	go.uber.org/atomic v1.11.0 // indirect
)
