# examples/saga-coordinator

> **SECURITY**: this is an EXAMPLE for learning the rho-kit
> multi-step transaction pattern. It uses in-memory idempotency
> and an in-process mutex for the exclusive section. Production
> deployments swap these for `data/idempotency/pgstore` (or
> `redisstore`) and `data/lock/pgadvisory.AcquireTx` respectively
> — see "Production wiring" below.

A reference rho-kit v2.0.0 service that demonstrates the canonical
multi-step transaction composition with compensation, idempotent
retry safety, and exclusive execution:

```
HTTP handler
  → exclusive section (per-key lock; in-process mutex; pgadvisory in prod)
    → idempotency cache lookup        → return cached on hit
    → saga.Run(definition, state)
         step 1: reserve-inventory    (Forward + Compensate)
         step 2: charge-card          (Forward + Compensate)
         step 3: dispatch-shipment    (Forward + Compensate)
      ↳ on failure: kit auto-runs Compensate on prior steps
                    in reverse order
    → cache the SUCCESSFUL result under the Idempotency-Key
```

The three primitives compose this way for distinct reasons:

1. **`saga.Run`** owns the forward/compensate orchestration.
   When step N fails, the kit invokes Compensate on steps
   N-1..0 in reverse, then returns a `*saga.ForwardError`
   joined with a `*saga.CompensateError` when any compensation
   also failed. Per-step Forward and Compensate callables MUST
   be idempotent — the kit may re-invoke them.

2. **`idempotency`** wraps the saga so retries from the same
   caller (same `Idempotency-Key` header) return the cached
   result. Without this, a network-level retry re-runs every
   step. Individual step idempotency is not enough — even
   idempotent steps still compound side-effects (two
   inventory holds, two charges) when called from two
   independent saga invocations.

3. **Exclusive section** prevents concurrent retries from racing
   each other into the same idempotency window. The saga
   itself is sequential; the lock prevents two callers from
   BOTH starting a saga before either's `Set` lands in the
   cache. In-process mutex here; `pgadvisory.AcquireTx` in
   production, where the lock is pinned to the database
   transaction the saga writes to.

## Failure routing

The kit's saga package surfaces two distinct error shapes that
deserve different operator responses:

| Shape | HTTP status | Meaning |
|---|---|---|
| `*saga.ForwardError` only | 422 Unprocessable Entity | Saga failed; all compensations succeeded. State is consistent. The upstream sender should fix the input (or the downstream) and retry. |
| `*saga.ForwardError` + `*saga.CompensateError` (joined) | 500 Internal Server Error | Saga failed AND one or more compensations also failed. State may be inconsistent. **Page an operator.** |

The handler in `app.go::writeSagaError` discriminates via
`errors.As`. The smoke test pins this contract via
`TestHandleOrder_SagaFailureReturns422`.

## Run

```bash
go run ./cmd/saga-coordinator
# Listens on :8097
```

## Exercise it

```bash
# Happy path: 3 steps run, response shows the full audit trail.
curl -s -X POST http://localhost:8097/orders \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: ord-1' \
  -d '{"order_id":"ord-1","amount":42.50,"currency":"USD"}' | jq

# Idempotent retry — same key returns cached state without
# re-executing the saga.
curl -s -X POST http://localhost:8097/orders \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: ord-1' \
  -d '{"order_id":"ord-1","amount":42.50,"currency":"USD"}' | jq

# List completed orders.
curl -s http://localhost:8097/orders | jq
```

## Smoke tests

```bash
go test ./examples/saga-coordinator/...
```

The tests cover nine contracts:

1. **Happy path** — all 3 steps run, state reflects every Forward.
2. **Compensation on step 3 failure** — kit invokes Compensate
   in reverse: charge-card → reserve-inventory. Failing step is
   NOT compensated (its Forward already failed).
3. **Compensation on step 2 failure** — partial rollback;
   step 1 compensates, step 3 never runs.
4. **Idempotent retry returns cached** — second call with the
   same key does not re-execute any step.
5. **Failure NOT cached** — a saga that failed-and-compensated
   does not poison the cache; a fresh retry can succeed.
6. **Concurrent retries serialize** — 8 goroutines hitting the
   same key result in exactly 1 saga execution.
7. **HTTP happy path** — full wire shape works.
8. **Missing `Idempotency-Key`** — 400 Bad Request.
9. **Saga failure returns 422** (not 500) when compensation
   succeeded — distinguishes "rolled back cleanly" from
   "needs operator intervention."

## Production wiring

Replace the in-memory store and mutex with the kit's persistent
equivalents:

```go
import (
    pgstore "github.com/bds421/rho-kit/data/v2/idempotency/pgstore"
    "github.com/bds421/rho-kit/data/v2/lock/pgadvisory"
)

idemStore := pgstore.New(db)
locker    := pgadvisory.New(db)

func (c *coordinator) runSaga(ctx context.Context, idemKey string, req OrderRequest) (*OrderState, error) {
    // Begin the transaction the saga's writes will land in.
    tx, _ := db.BeginTx(ctx, nil)
    defer tx.Rollback()

    // Pin the exclusive section to THIS transaction. Released on
    // COMMIT/ROLLBACK; auto-released on connection drop.
    if err := locker.AcquireTx(ctx, tx, idemKey); err != nil {
        return nil, err
    }

    // ... idempotency cache lookup via idemStore.Get ...
    // ... saga.Run inside the same tx so writes share the lock ...
    // ... idemStore.Set on success ...
    // ... tx.Commit ...
}
```

For crash-safety, wire each saga step that publishes a downstream
message through `infra/outbox` rather than calling the broker
directly. The wave-149 Multiplex relay handles redelivery if the
process crashes between commit and publish.

## What's NOT in this example

- **Persistent durability.** A process restart mid-saga loses
  the in-flight state. Production wires the saga state into a
  `saga_runs` table (one row per saga, status column) so a
  recovery loop can resume after a crash. The kit's
  `runtime/saga` package preamble documents this as a planned
  extension; until then, consumers compose outbox +
  idempotency + advisory lock as shown.
- **Per-step retry policies.** Each step uses the saga's
  outer retry behaviour (or none). For step-level retry, wrap
  the Forward callable with `resilience/retry.DoWith`.
- **Concurrent saga branches.** The kit's `Run` is sequential.
  A "fan out then join" pattern requires multiple saga
  Definitions running in parallel with a separate
  coordination step — not part of this example.
- **Observability.** Production wiring adds OTel spans around
  `saga.Run` (already emitted by the kit's wave-169 lifecycle
  span) and step-level histograms via consumer-side metric
  registration.
