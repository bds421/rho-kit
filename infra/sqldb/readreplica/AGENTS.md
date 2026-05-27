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

## Shutdown

`Close()` stops the background probe loop, waits for any in-flight
probes to complete, then closes every owned pool (primary + replicas)
exactly once. The wait is bounded by `WithProbeTimeout` per replica
— a `Close()` called while three replicas are mid-Ping waits at most
`probeTimeout` (default 5s) total, because probes run serially in the
loop. If you tune `WithProbeTimeout` upward for high-latency networks,
remember that `Close()` shutdown time scales with it.

`Close()` is idempotent. The caller MUST NOT reuse the supplied
Primary / Replicas pools after `Close()` — they're closed too, per
pgxpool's "Close once per pool" contract.

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
