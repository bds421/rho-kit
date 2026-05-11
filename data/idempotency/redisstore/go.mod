module github.com/bds421/rho-kit/data/idempotency/redisstore/v2

go 1.26.2

require github.com/redis/go-redis/v9 v9.18.0

require (
	github.com/bds421/rho-kit/data/v2 v2.0.0
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/klauspost/cpuid/v2 v2.2.5 // indirect
	go.uber.org/atomic v1.11.0 // indirect
)

replace github.com/bds421/rho-kit/data/v2 => ../../../data
