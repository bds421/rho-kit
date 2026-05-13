// Package redis wires a Redis connection into the Builder via the v2.0.0
// lazy-adapter Module API. Importing this package is the explicit declaration
// that the service needs Redis; HTTP-only services can omit it and avoid
// pulling go-redis/v9 into their binary.
//
// Usage:
//
//	import (
//	    "github.com/bds421/rho-kit/app/v2"
//	    "github.com/bds421/rho-kit/app/redis/v2"
//	    goredis "github.com/redis/go-redis/v9"
//	)
//
//	app.New("svc", "v1", base).
//	    With(redis.Module(&goredis.Options{Addr: cfg.RedisAddr})).
//	    Router(func(infra app.Infrastructure) http.Handler {
//	        conn := redis.Connection(infra)
//	        // …use the connection…
//	    }).
//	    Run()
//
// Transport safety (FR-077): the Module rejects non-loopback addresses that
// lack a TLSConfig or credentials. Local-dev fixtures may opt out with
// [WithoutTLS]. The check is unconditional otherwise.
package redis
