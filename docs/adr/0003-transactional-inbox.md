# ADR 0003: Transactional Inbox Semantics

## Status

Accepted.

## Context

Broker delivery is at least once. The kit has a transactional outbox but asks
every consumer to invent durable inbound deduplication and its transaction
boundary.

## Decision

The kit will provide a minimal inbox contract and a Postgres implementation.
The durable uniqueness key is `(consumer_name, message_id)`. A successful
inbox call performs the following atomically in the caller-provided database
transaction:

1. claim/record the incoming delivery;
2. execute the domain callback; and
3. allow the callback to write the Postgres outbox with the same transaction.

A duplicate claim is a normal `Duplicate` result and never invokes the domain
callback. Callback failure rolls back the claim and all local side effects, so
the broker can redeliver. A broker adapter must ACK only after the inbox call
commits successfully. The inbox does not claim exactly-once delivery or
external-side-effect atomicity.

Retention is mandatory configuration with documented operational ownership;
pruning is observable. The first implementation targets Postgres and the
existing pgx/outbox transaction context. Other backends need conformance proof
before becoming supported.

## Consequences

- Services put only local database effects and outbox writes inside the
  callback. Calls to third parties remain inherently at-least-once and must use
  their own idempotency key or a separate saga.
- Crash/retry tests must prove committed effects are not re-run and failed work
  remains retryable.
