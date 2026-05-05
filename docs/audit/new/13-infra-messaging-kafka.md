# NEW: infra/messaging/kafkabackend

**Phase**: 5 (Tier‑2 infrastructure)
**Module path**: `github.com/bds421/rho-kit/infra/messaging/kafkabackend`

## Why

Kafka is the de-facto standard for high-volume event pipelines and the natural target for outbox-driven event flow at scale. Teams shipping new event-driven products on rho-kit will want it.

## Approach

Use [`segmentio/kafka-go`](https://github.com/segmentio/kafka-go) (no CGo, pure Go, idiomatic). Provide:

```go
package kafkabackend

type Config struct {
    Brokers    []string
    ClientID   string
    TLS        *tls.Config
    SASL       sasl.Mechanism
}

type Publisher struct { /* ... */ }
func NewPublisher(cfg Config, opts ...PublisherOption) (*Publisher, error)

type Consumer struct { /* ... */ }
func NewConsumer(cfg Config, groupID string, topics []string) (*Consumer, error)
```

Behaviors to mirror:
- Publisher uses `kafka.Writer` with `RequiredAcks=All`, `Async=false` by default (correctness over throughput).
- Consumer commits offsets manually after handler returns (retain at-least-once semantics consistent with AMQP backend).
- Dead-letter via a `.dlq` topic with the same routing.
- Exposes idempotent producer mode (`enable.idempotence=true` equivalent in segmentio/kafka-go).

## Outbox integration

`infra/outbox/Relay` should accept any backend implementing `messaging.Publisher`. Once the Kafka publisher conforms, the existing outbox works.

## Builder integration

```go
func (b *Builder) WithKafka(cfg kafkabackend.Config) *Builder
```

## Definition of done

- [ ] Publisher + Consumer.
- [ ] `RequiredAcks=All` and manual offset commit defaults.
- [ ] DLQ topic pattern.
- [ ] Tests against Kafka container.
- [ ] Outbox `Relay` works with the Kafka publisher unchanged.
- [ ] Builder method.
- [ ] Recipe in `docs/ai/messaging.md`.
