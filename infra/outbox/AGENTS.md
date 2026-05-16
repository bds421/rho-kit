# AGENTS.md — `infra/outbox`

## When to use this package

- The service writes to a database AND publishes a message, and BOTH must be visible together (or not at all). Classic outbox pattern.
- Implementation that wraps the existing DB transaction; the outbox publisher reads from the outbox table asynchronously.
- Use with `outbox.MessagingPublisher` (wave 156) to route outbox entries to any kit messaging backend.

## When to use something else

- **Just need a queue, not transactional with DB:** `data/queue/redisqueue` or `data/queue/riverqueue` directly.
- **Two-phase commit semantics:** the kit doesn't ship 2PC; for distributed transactional state machines, look at workflow engines.

## Key APIs

- `outbox.Entry` — the persisted record. Includes `Topic`, `RoutingKey`, `Payload`, `Headers` (JSON-encoded).
- `outbox.Publisher` interface — implementations consume entries and publish them.
- `outbox.MessagingPublisher` (wave 156) — bridge that adapts any `messaging.Publisher` to `outbox.Publisher`. Drop-in for AMQP / Kafka / NATS / Redis backends.
- `outbox.Multiplex` (wave 149) — dispatcher with prefix-based routing to multiple publishers.

## Common mistakes

- **Writing to the outbox table OUTSIDE the business transaction** — defeats the pattern. The kit's `infra/outbox/postgres` accepts a `pgx.Tx` so the outbox insert participates in your existing transaction.
- **Hot-loop reads on the outbox table without indexes** — the kit ships the right migration; don't roll your own table schema.
- **`outbox.Multiplex` with overlapping prefixes** — the dispatcher picks the first match. Order your prefix routes from most-specific to least-specific.

## Observability

- `infra/outbox/postgres` exposes Prometheus metrics for retention cleanup (`outbox_retention_cleanup_seconds`, `outbox_entries_retained`).
- Publish-side metrics come from the wrapped backend (AMQP / Kafka / NATS / Redis).
