# Messaging — Cross-Service Durable Messaging

Packages: `infra/messaging` (interfaces), `infra/inbox/postgres` (transactional inbound deduplication), `infra/outbox` (transactional outbox), `infra/messaging/amqpbackend` (RabbitMQ), `infra/messaging/natsbackend` (NATS JetStream), `infra/messaging/kafkabackend` (Apache Kafka), `infra/messaging/redisbackend` (Redis Streams), `infra/messaging/membroker` (unit tests)

Snippet status: Go blocks in this recipe are illustrative fragments unless
explicitly introduced as generated or executable code. Buildable golden-path
evidence lives in `cmd/kit-new` scaffold tests and `examples/agentic-service`.

## When to Use

Use `infra/messaging` for **cross-service durable messaging**. The root package defines transport-agnostic interfaces (`Publisher`, `Consumer`, `Handler`, `Connector`). Backend implementations live in sub-packages.

| Backend | Use when |
|---|---|
| `amqpbackend` | Complex routing (topic/fanout/headers), DLX retry, publisher confirms, buffered publishing |
| `natsbackend` | NATS JetStream persistence, pull consumers, high-throughput event streams |
| `kafkabackend` | Apache Kafka (partitioned log, consumer-group fan-out, retention-driven replay, ordered per-key delivery). Retry / dead-letter topology from `Binding.Retry` is NOT honoured; wrap handlers in `resilience/retry`. |
| `redisbackend` | Redis Streams pub/sub (lighter weight, no extra broker) |
| `membroker` | Unit tests (in-memory, synchronous drain) |

If you only need in-process event dispatch, use `runtime/eventbus` instead.
If the message must be committed atomically with a database write, use
`infra/outbox` instead of direct publish or `BufferedPublisher`.

## Quick Start (AMQP)

```go
app.New(...).
    With(amqp.Module(cfg.AMQPURL)).
    Router(func(infra app.Infrastructure) http.Handler {
        // Declare topology (AMQP-specific)
        bindings, err := amqpbackend.DeclareAll(amqp.Connection(infra),
            messaging.BindingSpec{
                Exchange: "orders", ExchangeType: messaging.ExchangeDirect,
                Queue: "orders.created", RoutingKey: "order.created",
                Retry: &messaging.RetryPolicy{MaxRetries: 3, Delay: 30 * time.Second},
            },
        )

        // Start consumers in background — amqp.Consumer(infra) is pre-wired.
        // wg/shutdown are illustrative: wg coordinates the consumer goroutines
        // and shutdown signals the runner ctx to cancel.
        var wg sync.WaitGroup
        shutdown := func() { /* cancel runner ctx */ }
        messaging.StartConsumers(ctx, amqp.Consumer(infra), bindings,
            map[string]messaging.Handler{
                "order.created": handleOrderCreated,
            },
            wg, infra.Logger, shutdown,
        )

        // Publish from HTTP handlers
        mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
            msg, _ := messaging.NewMessage("order.created", order)
            amqp.Publisher(infra).Publish(r.Context(), "orders", "order.created", msg)
        })
    })
```

**Key point:** `amqp.Publisher(infra)` and `amqp.Consumer(infra)` are pre-wired by the Builder as `messaging.Publisher` and `messaging.Consumer` — no need to call `amqpbackend.NewPublisher` or `amqpbackend.NewConsumer` manually.

## Connection (AMQP)

```go
conn, err := amqpbackend.Connect(url, logger,
    amqpbackend.WithLazyConnect(),           // non-blocking startup (default in Builder)
    amqpbackend.WithMaxReconnectAttempts(0), // 0 = unlimited
    amqpbackend.WithTLS(tlsConfig),          // mTLS; amqp:// is upgraded to amqps://
    amqpbackend.WithURLProvider(vaultAMQPURL), // optional: called on every dial/reconnect
    amqpbackend.WithURLProviderTimeout(5*time.Second),
    amqpbackend.OnReconnect(func(c amqpbackend.Connector) error {
        return amqpbackend.DeclareAll(c, specs...) // re-declare after reconnect
    }),
)
```

`Connect` rejects plaintext `amqp://` unless `WithTLS` is supplied or
`WithoutTLS()` is set explicitly for local tests. Prefer
`amqps://` URLs in deployed configuration. Custom TLS configs are
cloned and raised to a TLS 1.2 minimum; stricter caller settings are
preserved.
For credential rotation, use `WithURLProvider` (or
`app/amqp.WithURLProvider`) so a fresh AMQP URL is fetched before the
initial dial and every reconnect. URL providers receive a bounded context; tune
that bound with `WithURLProviderTimeout` when the secret manager has a known
tail-latency SLO.

Reconnect backoff: 3s base, 2x multiplier, 60s max, ±25% jitter.

## Connection (NATS)

```go
conn, err := natsbackend.Connect(ctx, natsbackend.Config{
    URL: "tls://nats.internal:4222",
    Name: "orders",
    TLS: tlsConfig, // optional custom trust/client certs; cloned with TLS 1.2+ floor
    TokenProvider: tokenSource.Current, // optional rotating token callback
})
```

`Connect` rejects plaintext unauthenticated NATS unless TLS, auth
credentials, or `AllowInsecure` is configured. Use `AllowInsecure` only
for trusted single-host development setups. NATS URLs must use
`nats://`, `tls://`, `ws://`, or `wss://`, include a host, and must not
embed credentials, query parameters, or fragments; use `Username`,
`Token`, `CredentialsFile`, `NKeyFile`, `UsernamePasswordProvider`, or
`TokenProvider` for authentication.
`natsbackend.Config.Clone`, `Connect`, and `app/nats.Module`
snapshot caller-owned config; custom TLS configs are cloned with the TLS
1.2+ floor, and `ExtraOptions` slices are copied before storage/dial.
`UsernamePasswordProvider` and `TokenProvider` are delegated to nats.go auth
callbacks, so reconnects and server-triggered reauth use the latest value.

## Messages

```go
msg, _ := messaging.NewMessage("order.created", orderPayload) // UUID v7 ID, JSON payload
msg = msg.WithHeader(messaging.HeaderCorrelationID, corrID)    // immutable copy

var order Order
msg.DecodePayload(&order) // JSON decode
msg.CorrelationID()        // shorthand
```

Message metadata is validated before it reaches a broker or local
buffer. IDs and types are required UTF-8 tokens with no whitespace or
control bytes (`ID` <= 255 bytes, `Type` <= 256 bytes), payloads must
be valid JSON when set, and headers must be portable HTTP-style field
names with values capped at 8 KiB and no NUL/CR/LF bytes. Prefer
`messaging.NewMessage`; call `messaging.ValidateMessage` when you must
construct a `Message` manually.

Publish routes use the same shared boundary across AMQP, NATS, Redis,
`membroker`, and `BufferedPublisher`: exchange names are required,
routing keys may be empty only for fanout/exchange-only publishes, each
part is capped at 255 bytes, and whitespace, control bytes, and invalid
UTF-8 are rejected before a backend sees the message. Use
`messaging.ValidatePublishRoute(exchange, routingKey)` when accepting
operator-supplied route configuration outside the Builder.

## Message Size Limits

All messaging publishers enforce `messaging.DefaultMaxMessageBytes`
(1 MiB) before a message reaches the broker or the buffered-publisher
state file. The shared `messaging.MessageSizeLimiter` includes the JSON
message body plus transport headers in the estimate, so oversized
headers cannot bypass the body cap.

Use the adapter Modules' `WithMessageSizeLimiter` option for the golden path:

```go
sizeLimiter := messaging.NewMessageSizeLimiter(
    512<<10, // default
    messaging.MessageSizeRouteLimit{Exchange: "orders", RoutingKey: "order.bulk", MaxBytes: 8 << 20}, // exact override
)
app.New("orders", version, cfg.BaseConfig).
    With(amqp.Module(cfg.AMQPURL, amqp.WithMessageSizeLimiter(sizeLimiter))).
    With(nats.Module(natsCfg, nats.WithMessageSizeLimiter(sizeLimiter)))
```

Manual publishers expose the same pattern:

```go
pub := amqpbackend.NewPublisher(conn, logger,
    amqpbackend.WithMaxMessageBytes(512<<10),
    amqpbackend.WithRouteMaxMessageBytes("orders", "order.bulk", 8<<20),
)

natsPub := natsbackend.NewPublisher(conn,
    natsbackend.WithMaxMessageBytes(512<<10),
)

redisPub := redisbackend.NewPublisher(streamProducer,
    redisbackend.WithMaxMessageBytes(512<<10),
)

tests := membroker.New(membroker.WithMaxMessageBytes(512 << 10))
```

`messaging.NewBufferedPublisher` checks the same policy before direct
publish or buffering, preventing an over-large poison message from being
persisted and retried forever:

```go
buffered := messaging.NewBufferedPublisher(pub, conn, logger,
    messaging.WithStateFile("/var/data/buffered.json"),
    messaging.WithMaxMessageBytes(512<<10),
)
```

Use `WithoutMaxMessageBytes` only when another protocol or product
contract already enforces a smaller cap.

## Topology Declaration (AMQP)

```go
// Exchange + queue + binding + optional DLX retry infrastructure:
binding, _ := amqpbackend.DeclareTopology(conn, messaging.BindingSpec{
    Exchange:     "orders",
    ExchangeType: messaging.ExchangeDirect, // direct, fanout, topic, headers
    Queue:        "orders.created",
    RoutingKey:   "order.created",
    Retry: &messaging.RetryPolicy{MaxRetries: 3, Delay: 30 * time.Second},
})

// Multiple bindings at once:
bindings, _ := amqpbackend.DeclareAll(conn, spec1, spec2, spec3)

// Publisher-side only (no queues):
amqpbackend.DeclareExchanges(conn, messaging.ExchangeSpec{
    Exchange: "events", ExchangeType: messaging.ExchangeFanout,
})

// Pure computation (no broker connection needed):
bindings, _ := messaging.ComputeBindings(spec1, spec2) // for consumer-only services
```

`DeclareAll`, `ComputeBindings`, and `FindBinding` return detached binding
snapshots. Mutating the original `BindingSpec` slice or `RetryPolicy` pointer
after setup does not change the returned consumer bindings.

When `RetryPolicy` is set, DeclareAll creates:
- `{exchange}.retry` exchange + `{queue}.retry` queue (TTL → re-routes to main exchange)
- `{exchange}.dead` exchange + `{queue}.dead` queue (final destination for exhausted retries)

## Publishing (AMQP)

```go
// Manual wiring (not needed when using Builder):
pub := amqpbackend.NewPublisher(conn, logger)

err := pub.Publish(ctx, "orders", "order.created", msg) // confirms mode, waits for ACK
```

Messages are persistent (survive broker restart). Channel is lazily opened and recreated after reconnects.

## Consuming (AMQP)

```go
// Manual wiring (not needed when using Builder):
consumer := amqpbackend.NewConsumer(conn, publisher, logger,
    amqpbackend.WithPrefetch(10),
    amqpbackend.WithHooks(amqpbackend.ConsumerHooks{
        OnRetry:      func(msgID, msgType, queue string, retryCount int) {},
        OnDeadLetter: func(msgID, msgType, queue string, retryCount int) {},
    }),
)

// Resilient loop (reconnects on channel drop):
consumer.Consume(ctx, binding, func(ctx context.Context, d messaging.Delivery) error {
    var order Order
    if err := d.Message.DecodePayload(&order); err != nil {
        return apperror.NewPermanent("invalid payload") // ACK immediately, no retry
    }
    return processOrder(ctx, order) // error → retry via DLX
})
```

**Failure resolution:**
1. `apperror.PermanentError` → ACK immediately (no retry)
2. No RetryPolicy → ACK (discard)
3. Under MaxRetries → nack → DLX retry queue
4. MaxRetries exceeded → publish to dead exchange → ACK
5. Safety limit (MaxRetries × 3) → force ACK

## AMQP Prometheus Metrics

Builder-created RabbitMQ publishers and consumers are wired with these metrics
automatically. Manual AMQP publishers and consumers expose the same stable
Prometheus collectors:

```go
metrics := amqpbackend.NewMetrics(amqpbackend.WithRegisterer(prometheus.DefaultRegisterer))

pub := amqpbackend.NewPublisher(conn, logger,
    amqpbackend.WithPublisherMetrics(metrics),
)
consumer := amqpbackend.NewConsumer(conn, pub, logger,
    amqpbackend.WithConsumerMetrics(metrics),
)
```

Metric contract:

- `amqp_published_total{exchange,routing_key,outcome}`
- `amqp_publish_duration_seconds{exchange,routing_key,outcome}`
- `amqp_consumed_total{queue,outcome}`
- `amqp_handler_duration_seconds{queue,outcome}`

Publish outcomes are `success`, `failed`, `invalid_message`, `too_large`, and
`unroutable`. Consume outcomes are `acked`, `ack_failed`, `decode_error`,
`retry`, `dead_lettered`, `discarded`, `force_discarded`, and
`dlq_publish_failed`. Keep AMQP topology names static and low-cardinality;
never encode tenants, users, request IDs, or payload values into exchange,
routing-key, or queue names.

## NATS Prometheus Metrics

Builder-created NATS publishers are wired with these metrics automatically.
Manual publishers and consumers expose the same stable Prometheus collectors:

```go
metrics := natsbackend.NewMetrics(natsbackend.WithRegisterer(prometheus.DefaultRegisterer))

pub := natsbackend.NewPublisher(conn,
    natsbackend.WithPublisherMetrics(metrics),
)
consumer := natsbackend.NewConsumer(conn, natsbackend.ConsumerConfig{
    Stream:  "EVENTS",
    Durable: "orders",
}, logger,
    natsbackend.WithConsumerMetrics(metrics),
)
```

Metric contract:

- `nats_published_total{exchange,routing_key,outcome}`
- `nats_publish_duration_seconds{exchange,routing_key,outcome}`
- `nats_consumed_total{stream,durable,outcome}`
- `nats_handler_duration_seconds{stream,durable,outcome}`

Publish outcomes are `success`, `failed`, `invalid_message`, and `too_large`.
Consume outcomes are `acked`, `ack_failed`, `retry`, `nak_failed`,
`permanent`, `decode_error`, `handler_panic`, and `term_failed`. Handler
duration outcomes are `success`, `error`, and `panic`. Keep NATS stream,
durable, exchange, and routing-key names static and low-cardinality; never
encode tenants, users, request IDs, or payload values into NATS topology names.

## Redis Stream Prometheus Metrics

`infra/messaging/redisbackend` delegates to `data/stream/redisstream`, so direct
Redis Stream messaging emits the stream package metrics. Use custom registerers
for test isolation or multi-registry setups:

```go
producer := redisstream.NewProducer(redisClient,
    redisstream.WithProducerRegisterer(prometheus.DefaultRegisterer),
)
consumer, err := redisstream.NewConsumer(redisClient, "orders",
    redisstream.WithConsumerRegisterer(prometheus.DefaultRegisterer),
)
```

Metric contract:

- `redis_stream_messages_produced_total{stream}`
- `redis_stream_messages_consumed_total{stream,group}`
- `redis_stream_messages_failed_total{stream,group}`
- `redis_stream_messages_dead_lettered_total{stream,group}`
- `redis_stream_processing_duration_seconds{stream,group}`
- `redis_stream_pending_messages{stream,group}`

`stream` and `group` label values are opaque stable labels generated with
`promutil.OpaqueLabelValue`, so dashboards can group by stream/group without
exposing raw Redis topology names.

## StartConsumers (Convenience)

```go
messaging.StartConsumers(ctx, consumer, bindings,
    map[string]messaging.Handler{
        "order.created":  handleOrderCreated,
        "order.canceled": handleOrderCanceled,
    },
    wg, logger, shutdownFn,
)
// Returns error if any binding lacks a handler — catches config drift at startup.
```

## BufferedPublisher (At-Least-Once)

```go
pub := messaging.NewBufferedPublisher(publisher, conn, logger,
    messaging.WithMaxSize(10_000),
    messaging.WithStateFile("/var/data/buffered.json"), // crash-safe persistence
)
go func() {
    if err := pub.Run(ctx); err != nil {
        logger.Error("buffered publisher stopped", "err", err)
    }
}() // background drain loop

pub.Publish(ctx, "orders", "order.created", msg)
```

- Direct path: publishes immediately if buffer empty + broker healthy.
- Buffer path: appends to buffer on any failure condition.
- Drain loop: every 5s, processes up to 100 messages per cycle.
- State file: atomic write (temp + rename), survives crashes.
- Run loop: start exactly one per publisher; `Run` rejects nil contexts,
  uninitialized publishers, duplicate starts, and restarts after stop.
- Shutdown final drain uses a timeout-bounded detached run context, so broker
  wrappers still receive tenant, trace, logger, and other context values after
  cancellation.

## Transactional Outbox

Use `infra/outbox` when a domain write and downstream publish must succeed
or fail as one logical operation.

```go
store := mypg.NewOutboxStore(pool)
// NewWriter REQUIRES a tx-check predicate — atomicity is the whole point.
writer := outbox.NewWriter(store, requireTx)

err := pool.BeginTxFunc(ctx, pgx.TxOptions{}, func(tx pgx.Tx) error {
    txCtx := withTx(ctx, tx)
    if err := createOrder(txCtx, tx, order); err != nil {
        return err
    }
    return writer.Write(txCtx, outbox.WriteParams{
        Topic:       "orders",
        RoutingKey:  "order.created",
        MessageID:   order.ID,
        MessageType: "order.created",
        Payload:     payload,
    })
})
```

Run one or more relays as lifecycle components:

```go
relay := outbox.NewRelay(store, publisher, logger,
    outbox.WithRetention(7*24*time.Hour),
    outbox.WithFailedRetention(30*24*time.Hour),
)
runner.Add(relay)
```

The relay polls pending rows, retries transient failures with exponential
backoff, marks exhausted rows as failed, recovers stale processing rows, and
cleans old published/failed rows on startup plus periodic cleanup ticks. Keep
`WithFailedRetention` long enough for incident review.

## Transactional Inbox

Use `infra/inbox/postgres` at an at-least-once consumer boundary when the
delivery receipt, local writes, and optional outbox records must commit
together. `Process` starts the transaction; it returns `Duplicate: true` for
an already committed `(consumer, messageID)` and does not invoke the handler.

```go
inbox := inboxpostgres.New(pool)
store := outboxpostgres.New(pool)
writer := outbox.NewWriter(store, outboxpostgres.RequireTx)

result, err := inbox.Process(ctx, "orders.billing", delivery.ID, func(txCtx context.Context) error {
    tx, _ := outboxpostgres.TxFromContext(txCtx)
    if _, err := tx.Exec(txCtx, `INSERT INTO billing_projection ...`); err != nil {
        return err
    }
    return writer.Write(txCtx, outbox.WriteParams{
        Topic: "orders", RoutingKey: "billing.completed",
        MessageID: delivery.ID, MessageType: "BillingCompleted", Payload: payload,
    })
})
if err != nil {
    return err // do not ACK: the broker may redeliver
}
if result.Duplicate {
    return nil // ACK: this consumer already committed the delivery
}
return nil // ACK only after this successful return
```

For a caller-owned wider transaction, put its `pgx.Tx` in the context with
`outboxpostgres.WithTx` and call `ProcessInTx`; the caller then commits or
rolls back. Run `PruneBefore` as a scheduled retention task and monitor the
retained receipt count. `Inbox.HealthCheck()` is critical readiness because a
consumer must not ACK without durable receipts; the default Prometheus metrics
are `inbox_processed_total`, `inbox_duplicates_total`, and
`inbox_failures_total` (none use untrusted delivery labels). This provides effectively-once local effects, not
exactly-once effects against external systems.

## Debug HTTP Handlers (AMQP)

For development environments, `amqpbackend/debughttp` provides HTTP handlers to test messaging flows without a RabbitMQ client. Always mount them through `debughttp.Guard`; the guard only opens in the literal `development` environment and requires an authenticator.

```go
import "github.com/bds421/rho-kit/infra/messaging/amqpbackend/debughttp/v2"

debugAuth := debughttp.BasicAuth(map[string]string{
    cfg.DebugUser: cfg.DebugPassword,
})

// Dispatch a message directly to a consumer handler (bypasses RabbitMQ):
mux.Handle("POST /debug/consume", debughttp.Guard(
    cfg.Environment,
    debugAuth,
    debughttp.ConsumeHandler(cfg.Environment, debughttp.BasicAuth(map[string]string{"op": secret}), handlers, logger),
))

// List registered consumer message types:
mux.Handle("GET /debug/consume/types", debughttp.Guard(
    cfg.Environment,
    debugAuth,
    debughttp.ConsumeTypesHandler(handlers),
))

// Publish a message to a RabbitMQ exchange via REST:
mux.Handle("POST /debug/publish", debughttp.Guard(
    cfg.Environment,
    debugAuth,
    debughttp.PublishHandler(cfg.Environment, debughttp.BasicAuth(map[string]string{"op": secret}), amqp.Publisher(infra), []string{"orders"}, logger),
))
```

## Environment Variables

Configure via URL (takes precedence) or individual fields:

| Variable | Required | Default | Notes |
|---|---|---|---|
| `RABBITMQ_URL` | No* | — | Full AMQP URL, takes precedence over individual fields; prefer `amqps://` |
| `RABBITMQ_HOST` | No* | — | Hostname (used when RABBITMQ_URL is not set) |
| `RABBITMQ_PORT` | No | `5672` | Port |
| `RABBITMQ_USER` | No | `guest` | Username |
| `RABBITMQ_PASSWORD` | No | `guest` | Password (supports `_FILE` suffix for Docker secrets) |
| `RABBITMQ_VHOST` | No | `/` | Virtual host |

*Either `RABBITMQ_URL` or `RABBITMQ_HOST` must be set.

Loaded via `amqpbackend.LoadFields()`. Use `cfg.RabbitMQ.AMQPURL()` to get the resolved URL. Credentials are redacted in logs.

## Anti-Patterns

- **Never** ACK messages on transient errors — return the error so retry/DLX handles it.
- **Never** use plaintext AMQP in production — use `amqps://` or Builder TLS; `WithoutTLS()` is only for explicit local/test opt-in.
- **Never** lower broker TLS below TLS 1.2 — AMQP and NATS custom TLS configs are cloned and validated.
- **Never** put NATS credentials in the URL — use typed auth fields so logs stay redacted.
- **Never** use `apperror.Permanent` for transient failures — it skips all retries.
- **Never** create Publisher/Consumer outside the Router closure — the connection may not be ready.
- **Never** share AMQP channels across goroutines — Publisher serializes internally.
- **Never** forget to `Stop(ctx)` the broker `Connector` on shutdown — leaks channels and connections.
- **Never** call `amqpbackend.NewPublisher`/`NewConsumer` when using the Builder — use `amqp.Publisher(infra)`/`amqp.Consumer(infra)` instead.

## Testing

```go
//go:build integration

import kittestamqp "github.com/bds421/rho-kit/testing/kittest/v2/amqp"

func TestMessaging(t *testing.T) {
    url := kittestamqp.Start(t) // shared container per process
    exchange := "test-" + strings.ReplaceAll(t.Name(), "/", "-")
    // Use unique exchange/queue names per test — broker state leaks between tests.
}
```

Import path: `testing/kittest/v2/amqp` (package `amqp`, func `Start(t)`).
