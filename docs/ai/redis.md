# Redis — Cache, Streams, Queues, Distributed Locks

Packages: `infra/redis`, `data/cache/rediscache`, `data/cache/tenant`, `data/idempotency/redisstore`, `data/idempotency/tenant`, `data/stream/redisstream`, `data/queue/redisqueue`, `data/lock/redislock`

Snippet status: Go blocks in this recipe are illustrative fragments unless
explicitly introduced as generated or executable code. Buildable golden-path
evidence lives in `cmd/kit-new` scaffold tests and `examples/agentic-service`.

## When to Use

| Need | Package | Why |
|---|---|---|
| Cache frequently-read data with TTL | `data/cache/rediscache` | Shared across instances, implements `cache.Cache` interface |
| Scope cache or idempotency keys per tenant | `data/cache/tenant`, `data/idempotency/tenant` | Centralizes the length-prefixed tenant key encoder |
| Fan-out events to multiple consumers | `data/stream/redisstream` | Durable log, consumer groups, ordered delivery |
| Simple task queue (one consumer per item) | `data/queue/redisqueue` | FIFO with automatic retry, simpler than Streams |
| Distributed rate limiting | `data/ratelimit/redis` | Atomic GCRA shared across service replicas |
| Distributed mutual exclusion | `data/lock/redislock` | SET NX with Lua-script release, retry support |
| Cross-service messaging with routing | Use `infra/messaging` (RabbitMQ) instead | Complex routing, DLX, publisher confirms |
| Single-instance cache (no Redis needed) | Use `cache.MemoryCache` instead | In-process, no network hop |

## Connection

```go
// Option A: Use LoadRedisFields (recommended — reads env vars automatically)
fields, err := redis.LoadRedisFields()
opts, err := fields.Redis.Options() // converts to *goredis.Options

// Option B: Manual Options
opts := &goredis.Options{
    Addr:     "localhost:6379",
    Password: os.Getenv("REDIS_PASSWORD"),
    DB:       0,
}

conn, err := redis.Connect(opts,
    redis.Logger(logger),
    redis.WithInstance("cache"),               // Prometheus label
    redis.WithLazyConnect(),                   // non-blocking startup
    redis.WithHealthInterval(5*time.Second),
    redis.WithMaxReconnectAttempts(0),         // 0 = unlimited
    redis.WithOnReconnect(func(c *redis.Connection) error {
        return resubscribe(c.Client())
    }),
)
defer conn.Close()

client := conn.Client()    // redis.UniversalClient
conn.Healthy()             // bool
<-conn.Connected()         // wait for first success
<-conn.Dead()              // permanent failure
```

Health ping every 5s. Reconnect backoff: 3s base, 60s max.

URL validation: `redis.ValidateRedisURL("cache", rawURL)` — supports `redis://` and `rediss://` (TLS). Production configuration should use `rediss://` with credentials; parsed and caller-supplied TLS options are cloned and raised to a TLS 1.2 minimum before clients are created.
For Redis credential rotation, pass one of go-redis'
`CredentialsProvider`, `CredentialsProviderContext`, or
`StreamingCredentialsProvider` fields in `*redis.Options`; `app/redis.Module`
recognizes these as valid non-loopback credentials, and the go-redis client
handles new connection auth or streaming reauth according to the provider type.

## Environment Variables

Configure via URL (takes precedence) or individual fields:

| Variable | Required | Default | Notes |
|---|---|---|---|
| `REDIS_URL` | No* | — | Full Redis URL, takes precedence over individual fields |
| `REDIS_HOST` | No* | — | Hostname (used when REDIS_URL is not set) |
| `REDIS_PORT` | No | `6379` | Port |
| `REDIS_PASSWORD` | No | — | Password (secret) |
| `REDIS_DB` | No | `0` | Database number |
| `REDIS_ALLOW_PLAINTEXT` | No | `false` | Explicit local-dev opt-out for plaintext or anonymous Redis |

*Either `REDIS_URL` or `REDIS_HOST` must be set.

Loaded via `redis.LoadRedisFields()`. Use `fields.Redis.Options()` to get `*goredis.Options` for `Connect()`. Use `fields.Redis.RedisURL()` to get the resolved URL string. Credentials are redacted in logs (`slog.LogValuer`).

## Cache

Implements `cache.Cache` (Get/Set/Delete/Exists):

```go
redisCache, err := rediscache.NewCache(conn.Client(), "sessions",
    rediscache.WithCacheMaxValueSize(10 << 20), // default 10 MiB
)

err := redisCache.Set(ctx, "profile:123", jsonBytes, 30*time.Minute) // 0 TTL = no expiry
val, err := redisCache.Get(ctx, "profile:123")
if errors.Is(err, cache.ErrCacheMiss) { /* cold cache */ }

redisCache.Delete(ctx, "profile:123")
redisCache.Exists(ctx, "profile:123")
```

`rediscache` also implements `cache.BulkCache` (`MGet`, `MSet`, `SetNX`).
Bulk calls validate every key and reject batches above `cache.MaxBulkKeys`
before allocating result maps or sending Redis command batches.

For multi-tenant services, wrap the backend once and pass the wrapper
to route/repository code. The wrapper uses `core/tenant.Key` and
returns an error if the request context has no tenant ID.

```go
baseCache, err := rediscache.NewCache(conn.Client(), "sessions")
tenantCache := tenantcache.Wrap(baseCache)

err = tenantCache.Set(ctx, "profile:123", jsonBytes, 30*time.Minute)
```

Prometheus: `redis_cache_hits_total{name}`, `redis_cache_misses_total{name}`.

## Stream (Fan-Out Events)

Redis Streams with consumer groups, pending entry recovery, stale message claiming, dead-letter routing.

### Producer
```go
producer := stream.NewStreamProducer(conn.Client(),
    stream.WithMaxStreamLen(100_000),          // MAXLEN ~
    stream.WithRetention(7*24*time.Hour),      // MINID ~ (mutually exclusive with MaxLen)
    stream.WithProducerMaxPayloadSize(1<<20),  // 1 MiB
)

msg, _ := stream.NewStreamMessage("orders.created", orderPayload)
redisID, err := producer.Publish(ctx, "orders", msg)
ids, err := producer.PublishBatch(ctx, "orders", msgs) // pipeline
```

Stream message headers use the same portable metadata shape as
cross-service messaging: token-style names up to 128 bytes, values up to
8 KiB, and no NUL/CR/LF bytes. Message IDs and types are bounded, valid UTF-8,
and free of whitespace/control bytes; payloads are capped at 1 MiB by default
and must be valid JSON when present. `Message.WithHeader`, `Publish`, and
`PublishBatch` reject invalid headers before writing to Redis, including
when callers construct `Message{Headers: ...}` directly.
`PublishBatch` rejects batches above `stream.MaxBatchMessages` before building
the Redis pipeline, and `WithBatchSize` uses the same cap for consumer
`XREADGROUP`/`XAUTOCLAIM` fetches.

### Consumer
```go
consumer, err := stream.NewStreamConsumer(conn.Client(), "orders-group",
    stream.WithConsumerName("worker-1"),
    stream.WithBatchSize(10),
    stream.WithMaxRetries(5),
    stream.WithDeadLetterStream("orders.dead"),
    stream.WithClaimMinIdle(5*time.Minute),
    stream.WithClaimInterval(30*time.Second),
)

consumer.Consume(ctx, "orders", func(ctx context.Context, msg stream.StreamMessage) error {
    // nil → XACK
    // apperror.PermanentError → dead-letter immediately
    // other error → retry (up to MaxRetries) then dead-letter
    return processOrder(ctx, msg)
})

// Multiple streams:
stream.StartStreamConsumers(ctx, consumer,
    []stream.StreamBinding{
        {Stream: "orders",   Handler: handleOrder},
        {Stream: "payments", Handler: handlePayment},
    },
    wg, logger, shutdownFn,
)
```

On startup, processes pending entries from PEL first (crash recovery). Claim
loop runs every 30s. Consumers validate decoded entries before handler dispatch
and dead-letter malformed entries with `dl_reason=invalid_message`. During
shutdown or caller cancellation, handler grace windows and ACK/dead-letter
cleanup use bounded detached contexts that preserve parent context values such
as tenant, trace, and logger data.

## Queue (Simple Task Dispatch)

LIST-based FIFO with `BLMOVE` for crash-safe delivery:

```go
// imported as: import queue "github.com/bds421/rho-kit/data/queue/redisqueue/v2"
q := queue.NewQueue(conn.Client(),
    queue.WithMaxRetries(5),
    queue.WithMaxMessageBytes(1<<20),
    queue.WithDeadLetterQueue("tasks:dead"),
)

// Enqueue:
msg, _ := queue.NewMessage("email.send", emailPayload)
q.Enqueue(ctx, "tasks", msg)
q.EnqueueBatch(ctx, "tasks", msgs) // pipeline
q.Len(ctx, "tasks")                // queue depth

// Process (blocks until ctx cancelled):
q.Process(ctx, "tasks", func(ctx context.Context, msg queue.Message) error {
    // nil → removed from processing queue
    // error → re-enqueued (up to MaxRetries) then dead-lettered
    return sendEmail(ctx, msg)
})

// Multiple queues:
queue.StartProcessors(ctx, q,
    []queue.Binding{
        {Queue: "tasks",  Handler: handleTask},
        {Queue: "emails", Handler: handleEmail},
    },
    wg, logger, shutdownFn,
)
```

Use `queue.NewQueue` when startup code needs UUID/consumer-ID generation
failures returned as errors instead of panics. Passing `queue.WithConsumerID`
uses the supplied stable ID and skips auto-generation.

**Handlers must be idempotent** — messages can be delivered more than once after crashes.

Handler grace windows and post-handler ACK/retry/dead-letter cleanup use short
detached contexts during shutdown or caller cancellation. Those contexts preserve
parent values such as tenant, trace, and logger data, while Redis cleanup remains
bounded by the queue timeouts.

Queue names are validated as Redis keys and metric labels. `Enqueue` and
`EnqueueBatch` reject invalid message metadata before writing to Redis: IDs must
use the queue-safe token format, message types must be present and free of
whitespace/control bytes, and payloads are capped at 1 MiB by default via
`WithMaxMessageBytes`. Decoded processing-list entries are validated before
handler dispatch; malformed or non-portable stored entries are discarded instead
of being handed to application code.
`EnqueueBatch` rejects batches above `queue.MaxBatchMessages` before building
the Redis pipeline.

## Rate Limit

Redis-backed GCRA enforces one smoothed limit across all service replicas:

```go
limiter := redisrl.New(conn.Client(), time.Minute, 600,
    redisrl.WithKeyPrefix("orders:api:"),
    redisrl.WithRedisTime(), // optional: use Redis TIME to avoid node clock skew
)

allowed, retryAfter, err := limiter.Allow(ctx, tenantID)
```

Keys use the shared rate-limit key validator and prefixes reject whitespace,
control bytes, invalid UTF-8, and oversized values before reaching Redis. The
Lua script stores timestamps in microseconds because Redis Lua cannot precisely
represent current Unix nanosecond timestamps; sub-microsecond rates are rounded
up conservatively.

## Distributed Lock

```go
locker := redislock.NewLocker(conn.Client(),
    redislock.WithTTL(30*time.Second),            // default 30s
    redislock.WithRetry(500*time.Millisecond, 5), // poll 5 times at 500ms intervals
)

// Manual acquire/release:
lock, acquired, err := locker.Acquire(ctx, "my-resource")
if err != nil { return err }
if acquired {
    defer lock.Release(ctx)
    // critical section
}

// Convenience (acquire → fn → release):
err := locker.WithLock(ctx, "my-resource", func(ctx context.Context) error {
    return doExclusiveWork(ctx)
})
```

- Uses `SET NX` with TTL for acquiring
- Lua-script release ensures only the owner can unlock (unique random token)
- Token generation failures are returned as `Acquire` errors rather than panics
- `WithRetry` polls at interval and respects context cancellation
- `WithLock` and `LockerWithValue` release through a timeout-bounded detached
  caller context, so unlock cleanup survives cancellation while preserving
  tenant, trace, logger, and other context values

## Prometheus Metrics

All sub-packages register Prometheus metrics. Each accepts `prometheus.Registerer` via `WithRegisterer()` / `WithXxxRegisterer()` options for test isolation. Defaults to `prometheus.DefaultRegisterer`:
- Connection: `redis_command_duration_seconds`, `redis_pool_*`, `redis_connection_healthy`
- Cache: `redis_cache_hits_total`, `redis_cache_misses_total`
- Stream: `redis_stream_messages_produced_total`,
  `redis_stream_messages_consumed_total`,
  `redis_stream_messages_failed_total`,
  `redis_stream_messages_dead_lettered_total`,
  `redis_stream_processing_duration_seconds`,
  `redis_stream_pending_messages`
- Queue: `redis_queue_messages_enqueued_total`, `redis_queue_messages_processed_total`, `redis_queue_processing_depth`

Queue and stream metric labels use stable opaque values (`queue-<hash>`,
`stream-<hash>`, `group-<hash>`) instead of copying Redis key names into
Prometheus. Keep queue, stream, group, and cache names low-cardinality anyway;
opaque labels prevent cleartext leaks, not unbounded series creation.

## Anti-Patterns

- **Never** embed user IDs, request IDs, or other high-cardinality values in stream/queue/cache names — causes unbounded Prometheus label cardinality.
- **Never** use plaintext or anonymous Redis in production — use `rediss://` with credentials; `REDIS_ALLOW_PLAINTEXT=true` is only for trusted local fixtures.
- **Never** use `data/queue/redisqueue` when multiple consumer groups need the same events — use `data/stream/redisstream`.
- **Never** skip `apperror.NewPermanent()` for structurally invalid payloads — they'll retry forever.
- **Never** use `BLMOVE` timeout of 0 — it blocks indefinitely, preventing graceful shutdown.

## Testing

```go
//go:build integration

func TestRedisCache(t *testing.T) {
    url := redistest.Start(t) // import infra/redis/redistest/v2
    opts, _ := redis.ParseURL(url)
    conn, _ := redis.Connect(opts, redis.Logger(slog.Default()))
    defer conn.Close()

    c, _ := rediscache.NewCache(conn.Client(), "test-"+t.Name())
    // ... test cache operations
}
```
