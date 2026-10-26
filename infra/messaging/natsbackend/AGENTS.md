# AGENTS.md — `infra/messaging/natsbackend`

## When to use this package

- NATS JetStream is the broker — subject-based, push consumer model fits the workload.
- Per-subject retention + at-least-once delivery semantics.
- Native NATS auth (credentials file, JWT, NKey).

## When to use something else

- **AMQP / Kafka / Redis Streams:** see the respective backends.
- **Plain NATS (no JetStream):** out of scope — the kit only wraps the JetStream surface.

## Key APIs

- `NewPublisher(conn, opts...)` / `NewConsumer(conn, cfg ConsumerConfig, logger, opts...)`.
- Auth is configured on the `Config` struct passed to `Connect(ctx, cfg)`: set `CredentialsFile` for a NATS `.creds` JWT-NKey file, or `NKeyFile` for an NKey seed file (also `Username`/`Token`).
- `Fetch(ctx, js, stream, consumer, PullRequest{MaxMessages, MaxBytes, Expires, Heartbeat})` pulls one batch capped by count and bytes together (nats.go cannot combine both). It returns `*nats.Msg` whose `Ack`/`Nak`/`Term`/`Metadata` work; a missing consumer is `nats.ErrNoResponders`, a deleted one `jetstream.ErrConsumerDeleted`, a first message beyond `MaxBytes` `ErrMessageTooLarge`.
- `Subject(values...)` / `SubjectToken` / `ParseSubjectToken` escape arbitrary values into single tokens injectively (`[A-Za-z0-9_-]` stays as is); `ValidateSubject` / `ValidateFilter` check a whole subject or filter.

## Common mistakes

- **Treating retry as broker-side automatic** — NATS JetStream supports max-deliver but the kit consumer maps `messaging.Binding.Retry` to that. Without `WithoutRetry: true`, the default `Retry` policy applies.
- **Per-tenant stream / durable names (cardinality)** — opaque labels default since wave 140, but the JetStream config side still suffers if you have thousands of streams. Aggregate where possible.
- **Mixing JetStream with non-JetStream subjects** — this package assumes JetStream throughout.

## Observability

- Metrics: `nats_published_total`, `nats_publish_duration_seconds`, `nats_consumed_total`, `nats_handler_duration_seconds`. Labels `exchange`, `routing_key`, `outcome` (publish) / `stream`, `durable`, `outcome` (consume) — opaque since wave 140.
- OTel: `natsbackend/natstracing` for W3C trace context propagation.
