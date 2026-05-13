// Package app provides the standard service bootstrap and infrastructure wiring.
//
// It exists to keep every service main() identical: load config, wire logging,
// expose health/metrics, and shut down gracefully. The Builder fails fast on
// misconfiguration and ensures dependencies are ready before the public server
// starts accepting traffic. Background goroutines registered through
// Infrastructure.Background are tracked and drained on exit.
//
// # Lazy-Adapter Architecture (v2.0.0)
//
// Heavy adapter wiring (Postgres, Redis, RabbitMQ, NATS, OTel tracing, public
// gRPC) lives in per-adapter sub-modules under app/. Importing app/v2 alone no
// longer pulls pgx, go-redis, amqp091, nats.go, otelgrpc, or grpc-go into the
// binary; services declare each adapter they need with [Builder.With]:
//
//	import (
//	    "github.com/bds421/rho-kit/app/v2"
//	    "github.com/bds421/rho-kit/app/postgres/v2"
//	    "github.com/bds421/rho-kit/app/redis/v2"
//	    "github.com/bds421/rho-kit/app/amqp/v2"
//	)
//
//	app.New("svc", "v1", cfg).
//	    With(postgres.Module(pgxbackend.Config{DSN: dsn})).
//	    With(redis.Module(&goredis.Options{Addr: addr})).
//	    With(amqp.Module(brokerURL)).
//	    Router(...).
//	    Run()
//
// Sub-package getters retrieve the typed handle inside the RouterFunc:
//
//	pool := postgres.Pool(infra)
//	conn := redis.Connection(infra)
//	pub  := amqp.Publisher(infra)
//
// Light primitives (JWT/PASETO, signed requests, multi-tenant middleware,
// rate limiting, storage, audit log, cron, leader election, feature flags,
// SLO, action log, approval store, authz decider) stay on the Builder
// directly because their dep weight is bounded.
package app
