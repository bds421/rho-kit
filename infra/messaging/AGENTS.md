# AGENTS.md — `infra/messaging`

This is the **top-level messaging contract**, not a backend. Backend implementations live in sub-packages: `amqpbackend`, `kafkabackend`, `natsbackend`, `redisbackend`. The kit also provides an in-memory `membroker` for tests.

## When to use this package directly

- You need the kit-level `Publisher` / `Consumer` / `Handler` interfaces to write transport-agnostic service code.
- You're constructing a `Subscription` / `TypedSubscription[T]` / `SubscriptionGroup` (the wave-165 mid-level abstraction) — these wrap any `Consumer` implementation as a `lifecycle.Component`.
- You're declaring a `BindingSpec` / `Binding` — the topology descriptor every backend's `DeclareAll` (or equivalent) consumes.

## Mid-level abstraction: `Subscription` family

Prefer these over hand-wired `Consumer.Consume` calls:

- `NewSubscription(name, consumer, binding, handler, opts...)` — wraps the triple as a `lifecycle.Component`. Implements `Start(ctx)` / `Stop(ctx)`.
- `NewTypedSubscription[T](name, consumer, binding, typedHandler, opts...)` — decodes payload to `T` and validates via `validate.Struct` before dispatch. Decode/validate failures bypass the handler and surface to the consumer for nack/dead-letter routing.
- `NewSubscriptionGroup(logger)` — runs N subscriptions concurrently as one lifecycle row. Failure of any subscription cancels siblings; errors are joined into the returned chain.

## Common mistakes

- **Calling `Consumer.Consume` inside `lifecycle.NewFuncComponent(fn)` manually** — that's the pre-wave-165 pattern. `NewSubscription` handles the wrapping correctly (Single-Start guard, ctx cancellation, completion waiting, redacted errors).
- **JSON-decoding payloads in the handler** — use `TypedSubscription[T]` so decode + validate happen once at the kit boundary.
- **Setting `Retry` policy without checking backend support** — Kafka returns `ErrRetryUnsupported` from `Consume` (no broker-side retry primitive). Set `WithoutRetry: true` OR wrap the handler in `resilience/retry`.
- **Forgetting `outbox.MessagingPublisher` when the publish must be transactional with a DB write** — the wave-156 bridge turns any `messaging.Publisher` into an `outbox.Publisher`.

## Sentinels

- `ErrInvalidPublisher` / `ErrInvalidConsumer` — typed as `apperror.UnavailableError`; HTTP/gRPC adapters surface as 503/Unavailable.
- `ErrInvalidPublishContext` — typed as `apperror.ValidationError`; surfaces as 400/InvalidArgument.
- `ErrRetryUnsupported` — see above.
