# examples/background-worker

> **SECURITY**: this is an EXAMPLE for learning the canonical
> rho-kit async-worker composition. The binary uses an in-process
> fake Consumer so it stands up without a broker. Production
> deployments swap one of the kit's real backend Consumers
> (`amqpbackend`, `kafkabackend`, `natsbackend`, `redisbackend`).
> The wiring is unchanged across backends.

A reference rho-kit v2.0.0 service that demonstrates the canonical
async-worker composition:

```
messaging.TypedSubscription[OrderEvent]
  → circuitbreaker.ExecuteCtx     (downstream protection)
    → retry.DoWith                (transient-failure retry)
      → typed business handler    (record / publish / call downstream)
```

The wrapper order is load-bearing:

1. **Circuit breaker is OUTER.** When the downstream is broken,
   the breaker rejects fast with `ErrCircuitOpen` without burning
   retry attempts. The kit's wave 169 OTel tracing records
   `ErrCircuitOpen` as an attribute, not a span error — open
   circuits are steady-state, not exceptions.
2. **Retry is INNER.** Transient blips inside a half-open breaker
   still get a couple of attempts. With the breaker first, the
   retry policy never gets to multiply pain when the downstream is
   confirmed-down.
3. **TypedSubscription** decodes JSON to `OrderEvent` and runs the
   kit's `validate.Struct` BEFORE the resilient handler sees the
   payload. Invalid payloads are dropped without consuming the
   retry budget.

## Run

```bash
go run ./cmd/background-worker
# Blocks; the in-process fake consumer has no inputs unless you
# wire something to call its .Inject(...) method. The smoke test
# is the canonical way to exercise the wiring end-to-end.
```

## Smoke tests

```bash
go test ./examples/background-worker/...
```

The tests cover:
- Happy path: two valid events injected, both dispatched and
  recorded by the processor.
- Retry: a handler that fails on attempt 1 and succeeds on
  attempt 2 — the retry policy gives it the second chance.
- Lifecycle: the fake consumer returns cleanly when ctx is
  cancelled.

## Wiring a real backend

To swap the in-process fake for AMQP:

```go
import (
    "github.com/bds421/rho-kit/infra/v2/messaging/amqpbackend"
)

conn, err := amqpbackend.Dial(amqpURL)
// ... topology declarations ...
consumer := amqpbackend.NewConsumer(conn, /* opts */)

sub := messaging.NewTypedSubscription[OrderEvent](
    "orders-billing",
    consumer,                            // <-- changed
    binding,                             // <-- unchanged
    typed,                               // <-- unchanged
    messaging.WithSubscriptionLogger(logger),
)
```

The Subscription, the handler chain, the retry policy, the circuit
breaker — all unchanged. That's the whole point of the
`messaging.Consumer` interface and the wave-165 `TypedSubscription`
abstraction.

## What's NOT in this example

- **A real broker connection.** Use one of the four production
  backends and follow their per-package `AGENTS.md` for connect
  / topology / lifecycle guidance.
- **Leader-elected periodic work.** Some workers need a single
  replica to run a reconciliation loop. Add
  `infra/leaderelection/k8slease` (or `etcd`, `pgadvisory`,
  `redislock`) and gate the periodic job's `OnAcquired` callback.
  See the leader-election runbook
  (`docs/ai/runbooks/leader-election.md`) for the
  callback-drain contract.
- **Outbox dispatch on the producer side.** This service is
  consumer-only. The matching producer pattern uses
  `infra/outbox` so the publish step is crash-safe.
- **Metrics + OTel tracing.** Wired automatically through the
  `messaging.WithSubscriptionLogger` integration in real
  backends; for fakeConsumer in this example, the metrics are
  inert.
