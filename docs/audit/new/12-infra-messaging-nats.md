# NEW: infra/messaging/natsbackend

**Phase**: 5 (Tier‑2 infrastructure)
**Module path**: `github.com/bds421/rho-kit/infra/messaging/natsbackend`

## Why

The kit has AMQP and Redis Streams backends. NATS JetStream is the natural third — high-throughput pub/sub with persistence, simpler than Kafka. Many teams reach for NATS once their AMQP load surpasses single-node RabbitMQ.

## Public API

Mirrors `amqpbackend.Publisher` / `amqpbackend.Consumer` so the higher-level `messaging.Publisher`/`messaging.Consumer` interfaces work unchanged:

```go
package natsbackend

type Config struct {
    URL          string
    StreamName   string
    Subjects     []string
    Retention    nats.RetentionPolicy
    MaxAge       time.Duration
    MaxBytes     int64
    StorageType  nats.StorageType
}

type Publisher struct { /* ... */ }
func NewPublisher(cfg Config) (*Publisher, error)

type Consumer struct { /* ... */ }
func NewConsumer(cfg Config, durable string) (*Consumer, error)
```

Behaviors to mirror from amqpbackend:
- Publisher confirms (JetStream `PublishMsg` with ack).
- Consumer prefetch (max-in-flight via JetStream consumer config).
- Retry/DLQ semantics analogous to AMQP DLX (use a dedicated `.dlq` stream).
- Reconnection with backoff.

## Builder integration

```go
// app.Builder gains:
func (b *Builder) WithNATS(cfg natsbackend.Config) *Builder
```

## Definition of done

- [x] Publisher + Consumer + topology (`Connection.EnsureStream`). ✅ this PR
- [x] Retry / DLQ pattern: handler errors nack so JetStream redelivers after AckWait; malformed messages are Term'd to prevent poison-message loops; integration test asserts `Delivery.Redelivered=true` on second attempt.
- [x] Integration tests behind `//go:build integration` covering round trip + nack-redeliver via testcontainers nats:2.11-alpine.
- [ ] Builder method `WithNATS` (deferred — primitive ships first).
- [ ] Recipe in `docs/ai/messaging.md` (deferred to docs sweep).
