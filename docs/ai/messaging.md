# Messaging — Cross-Service Durable Messaging

Packages: `infra/messaging` (interfaces), `messaging/amqpbackend` (RabbitMQ), `messaging/redisbackend` (Redis Streams), `messaging/wmconvert` (Watermill adapter), `messaging/membroker` (unit tests)

## When to Use

Use `infra/messaging` for **cross-service durable messaging**. The root package defines transport-agnostic interfaces (`MessagePublisher`, `MessageConsumer`, `Handler`, `Connector`). Backend implementations live in sub-packages.

| Backend | Use when |
|---|---|
| `amqpbackend` | Complex routing (topic/fanout/headers), DLX retry, publisher confirms, buffered publishing |
| `redisbackend` | Redis Streams pub/sub (lighter weight, no extra broker) |
| `wmconvert` | Any [Watermill](https://github.com/ThreeDotsLabs/watermill)-supported broker (Kafka, NATS, Google Pub/Sub, SQL) via `wmconvert.NewBackend` |
| `membroker` | Unit tests (in-memory, synchronous drain) |

If you only need in-process event dispatch, use `runtime/eventbus` instead.

## Watermill Integration

The `wmconvert` package provides a generic adapter that wraps any [Watermill](https://github.com/ThreeDotsLabs/watermill) publisher/subscriber behind rho-kit's messaging interfaces. Use this for provider portability when you don't need backend-specific features.

```go
import (
    "github.com/ThreeDotsLabs/watermill-kafka/v3/pkg/kafka"
    "github.com/bds421/rho-kit/infra/messaging/wmconvert"
)

// Create Watermill Kafka publisher/subscriber
kafkaPub, _ := kafka.NewPublisher(kafkaConfig, watermillLogger)
kafkaSub, _ := kafka.NewSubscriber(kafkaConfig, watermillLogger)

// Wrap as rho-kit messaging interfaces
backend := wmconvert.NewBackend(kafkaPub, kafkaSub, logger,
    wmconvert.WithTopicFunc(wmconvert.RoutingKeyTopic),
    wmconvert.WithHealthFunc(func() bool { return kafkaPub.Connected() }),
)

// Use exactly like amqpbackend or redisbackend
err := backend.Publisher().Publish(ctx, "exchange", "user.created", msg)
```

Available topic functions: `ExchangeTopic` (default, maps to exchange/stream name), `RoutingKeyTopic` (maps to routing key), `CombinedTopic` (exchange.routingKey).

**When NOT to use wmconvert:**
- AMQP with DLX retry counting (x-death headers are AMQP-specific)
- Redis Streams with dead-letter routing, XCLAIM, or MINID trimming
- When you need `BufferedPublisher` or `PublishRaw`

## Quick Start (AMQP)

```go
app.New(...).
    WithRabbitMQ(cfg.AMQPURL).
    Router(func(infra app.Infrastructure) http.Handler {
        // Declare topology (AMQP-specific)
        bindings, err := amqpbackend.DeclareAll(infra.Broker.(*amqpbackend.Connection),
            messaging.BindingSpec{
                Exchange: "orders", ExchangeType: messaging.ExchangeDirect,
                Queue: "orders.created", RoutingKey: "order.created",
                Retry: &messaging.RetryPolicy{MaxRetries: 3, Delay: 30 * time.Second},
            },
        )

        // Start consumers in background — infra.Consumer is pre-wired
        messaging.StartConsumers(ctx, infra.Consumer, bindings,
            map[string]messaging.Handler{
                "order.created": handleOrderCreated,
            },
            wg, infra.Logger, shutdown,
        )

        // Publish from HTTP handlers
        mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
            msg, _ := messaging.NewMessage("order.created", order)
            infra.Publisher.Publish(r.Context(), "orders", "order.created", msg)
        })
    })
```

**Key point:** `infra.Publisher` and `infra.Consumer` are pre-wired by the Builder as `messaging.MessagePublisher` and `messaging.MessageConsumer` — no need to call `amqpbackend.NewPublisher` or `amqpbackend.NewConsumer` manually.

## Connection (AMQP)

```go
conn, err := amqpbackend.Dial(url, logger,
    amqpbackend.WithLazyConnect(),           // non-blocking startup (default in Builder)
    amqpbackend.WithMaxReconnectAttempts(0), // 0 = unlimited
    amqpbackend.WithTLS(tlsConfig),          // mTLS
    amqpbackend.OnReconnect(func(c amqpbackend.Connector) error {
        return amqpbackend.DeclareAll(c, specs...) // re-declare after reconnect
    }),
)
```

Reconnect backoff: 3s base, 2x multiplier, 60s max, ±25% jitter.

## Messages

```go
msg, _ := messaging.NewMessage("order.created", orderPayload) // UUID v7 ID, JSON payload
msg = msg.WithHeader(messaging.HeaderCorrelationID, corrID)    // immutable copy

var order Order
msg.DecodePayload(&order) // JSON decode
msg.CorrelationID()        // shorthand
```

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

When `RetryPolicy` is set, DeclareAll creates:
- `{exchange}.retry` exchange + `{queue}.retry` queue (TTL → re-routes to main exchange)
- `{exchange}.dead` exchange + `{queue}.dead` queue (final destination for exhausted retries)

## Publishing (AMQP)

```go
// Manual wiring (not needed when using Builder):
pub := amqpbackend.NewPublisher(conn, logger)
defer pub.Close()

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
    messaging.WithBufferedMaxSize(10_000),
    messaging.WithBufferedStateFile("/var/data/buffered.json"), // crash-safe persistence
)
go pub.Run(ctx) // background drain loop

pub.Publish(ctx, "orders", "order.created", msg)
```

- Direct path: publishes immediately if buffer empty + broker healthy.
- Buffer path: appends to buffer on any failure condition.
- Drain loop: every 5s, processes up to 100 messages per cycle.
- State file: atomic write (temp + rename), survives crashes.

## Debug HTTP Handlers (AMQP)

For development/staging environments, `amqpbackend/debughttp` provides HTTP handlers to test messaging flows without a RabbitMQ client. Import the optional `debughttp` package:

```go
import "github.com/bds421/rho-kit/infra/messaging/amqpbackend/debughttp"

// Dispatch a message directly to a consumer handler (bypasses RabbitMQ):
mux.HandleFunc("POST /debug/consume", debughttp.ConsumeHandler(handlers, logger))

// List registered consumer message types:
mux.HandleFunc("GET /debug/consume/types", debughttp.ConsumeTypesHandler(handlers))

// Publish a message to a RabbitMQ exchange via REST:
mux.HandleFunc("POST /debug/publish", debughttp.PublishHandler(infra.Publisher, allowedExchanges, logger))
```

## Environment Variables

Configure via URL (takes precedence) or individual fields:

| Variable | Required | Default | Notes |
|---|---|---|---|
| `RABBITMQ_URL` | No* | — | Full AMQP URL, takes precedence over individual fields |
| `RABBITMQ_HOST` | No* | — | Hostname (used when RABBITMQ_URL is not set) |
| `RABBITMQ_PORT` | No | `5672` | Port |
| `RABBITMQ_USER` | No | `guest` | Username |
| `RABBITMQ_PASSWORD` | No | `guest` | Password (supports `_FILE` suffix for Docker secrets) |
| `RABBITMQ_VHOST` | No | `/` | Virtual host |

*Either `RABBITMQ_URL` or `RABBITMQ_HOST` must be set.

Loaded via `amqpbackend.LoadRabbitMQFields()`. Use `cfg.RabbitMQ.AMQPURL()` to get the resolved URL. Credentials are redacted in logs.

## Anti-Patterns

- **Never** ACK messages on transient errors — return the error so retry/DLX handles it.
- **Never** use `apperror.Permanent` for transient failures — it skips all retries.
- **Never** create Publisher/Consumer outside the Router closure — the connection may not be ready.
- **Never** share AMQP channels across goroutines — Publisher serializes internally.
- **Never** forget `defer pub.Close()` — leaks AMQP channels.
- **Never** call `amqpbackend.NewPublisher`/`NewConsumer` when using the Builder — use `infra.Publisher`/`infra.Consumer` instead.

## Testing

```go
//go:build integration

func TestMessaging(t *testing.T) {
    url := rabbitmqtest.Start(t) // shared container per process
    exchange := "test-" + strings.ReplaceAll(t.Name(), "/", "-")
    // Use unique exchange/queue names per test — broker state leaks between tests.
}
```

Import path: `messaging/amqpbackend/rabbitmqtest`.
