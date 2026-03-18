# Redis — Cache, Streams, Queues, Distributed Locks

Packages: `infra/redis`, `data/cache/rediscache`, `data/stream/redisstream`, `data/queue/redisqueue`, `data/lock/redislock`

## When to Use

| Need | Package | Why |
|---|---|---|
| Cache frequently-read data with TTL | `data/cache/rediscache` | Shared across instances, implements `cache.Cache` interface |
| Fan-out events to multiple consumers | `data/stream/redisstream` | Durable log, consumer groups, ordered delivery |
| Simple task queue (one consumer per item) | `data/queue/redisqueue` | FIFO with automatic retry, simpler than Streams |
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
    redis.WithLogger(logger),
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

URL validation: `redis.ValidateRedisURL("cache", rawURL)` — supports `redis://` and `rediss://` (TLS).

## Environment Variables

Configure via URL (takes precedence) or individual fields:

| Variable | Required | Default | Notes |
|---|---|---|---|
| `REDIS_URL` | No* | — | Full Redis URL, takes precedence over individual fields |
| `REDIS_HOST` | No* | — | Hostname (used when REDIS_URL is not set) |
| `REDIS_PORT` | No | `6379` | Port |
| `REDIS_PASSWORD` | No | — | Password (secret) |
| `REDIS_DB` | No | `0` | Database number |

*Either `REDIS_URL` or `REDIS_HOST` must be set.

Loaded via `redis.LoadRedisFields()`. Use `fields.Redis.Options()` to get `*goredis.Options` for `Connect()`. Use `fields.Redis.RedisURL()` to get the resolved URL string. Credentials are redacted in logs (`slog.LogValuer`).

## Cache

Implements `cache.Cache` (Get/Set/Delete/Exists):

```go
redisCache, err := cache.NewRedisCache(conn.Client(), "sessions",
    cache.WithCacheMaxValueSize(10 << 20), // default 10 MiB
)

err := redisCache.Set(ctx, "user:123", jsonBytes, 30*time.Minute) // 0 TTL = no expiry
val, err := redisCache.Get(ctx, "user:123")
if errors.Is(err, cache.ErrCacheMiss) { /* cold cache */ }

redisCache.Delete(ctx, "user:123")
redisCache.Exists(ctx, "user:123")
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

On startup, processes pending entries from PEL first (crash recovery). Claim loop runs every 30s.

## Queue (Simple Task Dispatch)

LIST-based FIFO with `BLMOVE` for crash-safe delivery:

```go
q := queue.NewQueue(conn.Client(),
    queue.WithQueueMaxRetries(5),
    queue.WithMaxPayloadSize(1<<20),
    queue.WithDeadLetterQueue("tasks:dead"),
)

// Enqueue:
msg, _ := queue.NewQueueMessage("email.send", emailPayload)
q.Enqueue(ctx, "tasks", msg)
q.EnqueueBatch(ctx, "tasks", msgs) // pipeline
q.Len(ctx, "tasks")                // queue depth

// Process (blocks until ctx cancelled):
q.Process(ctx, "tasks", func(ctx context.Context, msg queue.QueueMessage) error {
    // nil → removed from processing queue
    // error → re-enqueued (up to MaxRetries) then dead-lettered
    return sendEmail(ctx, msg)
})

// Multiple queues:
queue.StartQueueProcessors(ctx, q,
    []queue.QueueBinding{
        {Queue: "tasks",  Handler: handleTask},
        {Queue: "emails", Handler: handleEmail},
    },
    wg, logger, shutdownFn,
)
```

**Handlers must be idempotent** — messages can be delivered more than once after crashes.

## Distributed Lock

```go
l := lock.New(conn.Client(), "my-resource",
    lock.WithTTL(30*time.Second),           // default 30s
    lock.WithRetry(500*time.Millisecond, 5), // poll 5 times at 500ms intervals
)

// Manual acquire/release:
acquired, err := l.Acquire(ctx)
if acquired {
    defer l.Release(ctx)
    // critical section
}

// Convenience (acquire → fn → release):
err := l.WithLock(ctx, func(ctx context.Context) error {
    return doExclusiveWork(ctx)
})
```

- Uses `SET NX` with TTL for acquiring
- Lua-script release ensures only the owner can unlock (unique random token)
- `WithRetry` polls at interval, respects context cancellation

## Prometheus Metrics

All sub-packages register Prometheus metrics. Each accepts `prometheus.Registerer` via `WithRegisterer()` / `WithXxxRegisterer()` options for test isolation. Defaults to `prometheus.DefaultRegisterer`:
- Connection: `redis_command_duration_seconds`, `redis_pool_*`, `redis_connection_healthy`
- Cache: `redis_cache_hits_total`, `redis_cache_misses_total`
- Stream: `redis_stream_messages_produced_total`, `redis_stream_messages_consumed_total`, `redis_stream_pending_messages`
- Queue: `redis_queue_messages_enqueued_total`, `redis_queue_messages_processed_total`, `redis_queue_processing_depth`

## Anti-Patterns

- **Never** embed user IDs, request IDs, or other high-cardinality values in stream/queue/cache names — causes unbounded Prometheus label cardinality.
- **Never** use `data/queue/redisqueue` when multiple consumer groups need the same events — use `data/stream/redisstream`.
- **Never** skip `apperror.NewPermanent()` for structurally invalid payloads — they'll retry forever.
- **Never** use `BLMOVE` timeout of 0 — it blocks indefinitely, preventing graceful shutdown.

## Testing

```go
//go:build integration

func TestRedisCache(t *testing.T) {
    url := redistest.Start(t) // shared container per process
    opts, _ := redis.ParseURL(url)
    conn, _ := redis.Connect(opts, redis.WithLogger(slog.Default()))
    defer conn.Close()

    c, _ := cache.NewRedisCache(conn.Client(), "test-"+t.Name())
    // ... test cache operations
}
```
