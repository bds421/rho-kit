# Runbook: Redis pool timeouts

Covers `RhoKitRedisPoolTimeouts` (pool-timeout increase > 5/5m for
10m, warn).

Snippet status: PromQL blocks in this runbook are illustrative query fragments
to adapt to the service's Prometheus labels and SLO windows.

## What this alert says

The application's Redis client tried to acquire a connection from
its local pool, waited up to the pool's wait timeout, and gave up.
Each timeout becomes a failed Redis command, which usually surfaces
upstream as a 5xx or a degraded user experience (cache miss → DB
hit on the slow path).

## Likely causes (in order of frequency)

1. **Pool too small for current load** — confirm on the Redis
   dashboard: `redis_pool_total_conns` is at PoolSize, idle
   connections are 0, command rate is high. The pool cannot keep
   up; raising `PoolSize` would help.
2. **Redis server is slow** — confirm: `redis_command_duration_seconds`
   p99 climbed at the same time. Each command holds its connection
   longer, so the pool drains. Often Redis CPU saturated, or a
   `KEYS *` / large `SUNIONSTORE` blocking the main thread.
3. **Network latency to Redis** — confirm: command p99 is high
   *and* reconnect attempts are non-zero. TCP-level slowness or
   transient drops.
4. **Long-running blocking command** — confirm: a command like
   `BLPOP` with a long timeout holds a connection for the full
   duration. If your codebase uses blocking commands, they should
   use a separate Redis client to avoid starving the main pool.
5. **Connection leak in the application** — confirm: total
   connections climb monotonically without dropping. Less common
   with `go-redis/v9` since pooling is automatic, but possible if
   custom hooks hold a reference.

## Immediate response

1. Open the Redis dashboard.
2. Read off command p99 and the total/idle/stale connection panel.
3. If p99 is normal but pool is saturated: raise `PoolSize` (the
   Redis client option). Default is 10 per CPU; bump to 20-30 for
   high-throughput services.
4. If p99 is high: the Redis server itself is slow. Check Redis
   server-side `INFO commandstats`, look for the slowest command
   group. Common culprits: `KEYS`, large `MULTI/EXEC`, slow Lua
   scripts.
5. If reconnects are spiking: investigate network path (TCP
   retransmits, recent infra change). Each reconnect blocks the
   command for the dial timeout.
6. Page the team responsible for Redis if the server is unhealthy.

## Longer-term fix

- Set `PoolSize` based on observed peak in-use connections × 1.5,
  not the default.
- Move blocking commands (BLPOP, XREAD long timeouts) to a
  dedicated Redis client with its own pool.
- Add per-command `context.WithTimeout` so a single slow Redis
  call cannot wedge the calling request.
- Consider client-side caching for read-heavy workloads to reduce
  Redis QPS.

## Related metrics / queries

```promql
# Pool timeouts gained in last 5 minutes (per service / redis_instance)
increase(redis_pool_timeouts[5m])

# Pool utilisation (1.0 = saturated)
sum by (namespace, service, redis_instance) (
  redis_pool_total_conns
  - redis_pool_idle_conns
)
/ clamp_min(
    sum by (namespace, service, redis_instance) (redis_pool_total_conns),
    1
  )

# Command p99 latency
redis_command_duration_seconds:p99:5m
```
