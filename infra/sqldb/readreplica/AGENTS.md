# infra/sqldb/readreplica

## Purpose

Postgres read/write routing across one primary + N replicas. Round-robin
across healthy replicas for read-only Acquire calls; fall back to
primary when no replica is healthy. Background health-probe loop
removes failing replicas from rotation and re-adds them on recovery.

## Public API

- `New(cfg Config, opts ...Option) (*RoutingPool, error)`
- `RoutingPool.Acquire(ctx, opts ...AcquireOption) (*pgxpool.Conn, error)`
- `RoutingPool.Close()`
- `RoutingPool.PrimaryHealthy(ctx) bool`
- `RoutingPool.ReplicaHealth() []bool`
- Options: `WithHealthInterval`, `WithMaxConsecutiveFailures`,
  `WithProbeTimeout`, `WithLogger`, `WithMetricsRegisterer`,
  `WithoutHealthCheck`
- Acquire options: `WithReadOnly`
- `Acquirer` interface: minimal surface (Acquire/Ping/Close) so callers
  pass real *pgxpool.Pool or test fakes

## Routing rules

| AcquireOption | Healthy replicas? | Where the conn comes from |
|---|---|---|
| (none — default) | n/a | primary |
| WithReadOnly | yes | round-robin healthy replica |
| WithReadOnly | no  | primary (fallback, logs warn, increments metric) |

## Health rules

- Each replica starts healthy.
- A replica's consecutive-failure counter increments on every failed
  Acquire AND every failed periodic Ping.
- At `WithMaxConsecutiveFailures` (default 3) the replica is removed
  from rotation.
- The background health loop (default 30s ticker) re-probes unhealthy
  replicas; one successful Ping re-adds.

## Metrics (Prometheus)

- `sqldb_readreplica_primary_acquires_total`
- `sqldb_readreplica_replica_acquires_total`
- `sqldb_readreplica_replica_fallback_total`
- `sqldb_readreplica_replicas_healthy` (gauge)
- `sqldb_readreplica_replicas_total` (gauge)

Pass `WithMetricsRegisterer(prometheus.NewRegistry())` in tests.

## Tests

`go test -race ./...` from this directory. Covers:

- New rejects nil primary
- Default Acquire routes to primary
- Read-only Acquire round-robins across replicas
- Fallback to primary when all replicas mark unhealthy
- Health loop re-adds a replica after recovery
- No replicas: pass-through to primary
- Close is idempotent
- Concurrent reads (50 goroutines × 50 ops) don't race

## See also

- `infra/sqldb/pgx` — single-pool wrapper. The Acquirer interface here
  is satisfied by `*pgxpool.Pool` directly.
- `app/postgres` — Builder adapter. A future `WithReadReplicas` option
  will thread a RoutingPool through the kit lifecycle.
