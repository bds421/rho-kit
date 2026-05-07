# infra/messaging — AMQP backend, buffered publisher, debug HTTP, membroker

## Landed

- ✅ **AMQP publisher `mandatory=true` + `NotifyReturn`** — unroutable messages now surface as `ErrUnroutable` to the caller instead of being silently ack'd (commit `068eeb5`).
- ✅ **`debughttp` Guard middleware** — refuses to register in production env, requires an injected `Authenticator` (BasicAuth or AllowFromHeader, both with constant-time compare) (commit `068eeb5`).
- ✅ **Topology rejects sub-millisecond `Retry.Delay`** — eliminates the "TTL truncates to 0 → tight redelivery loop" bug (commit `068eeb5`).

## Open

_Closed — see Recently Landed below._

## Recently Landed (Phase 3, commit `a0b70f0`)

- ✅ **AMQP handler ctx during shutdown** — `context.WithoutCancel(parent) + WithTimeout` preserves trace IDs / values; `IsShutdown(ctx)` gives handlers a sentinel they can branch on.
- ✅ **DLQ failure cap** — `WithMaxDLQConsecutiveFailures(n)` (default 10) force-discards after N consecutive dead-publish failures, breaking the bounce-loop. Operators get a clear error log + counter rather than ~15 min of CPU thrash.
- ✅ **`Connection.WaitForConnection(ctx)`** — 100ms-poll helper; outbox Relay can pause poll on reconnect rather than burning retry budget.
- ✅ **BufferedPublisher fixes** — `directInFlight` reservation skipped when `maxSize=1`; `LastSaveError()` (atomic.Pointer[error]) surfaces save failures to callers; documented 0o600 + JSON state-file format on disk.
- ✅ **membroker `SubscriptionID` + `Unsubscribe`** — Subscribe returns an ID; tests no longer accumulate stale handlers.

xDeath / actionDiscard items remain on the longer-term list (deferred — they touch the retry-queue naming contract; not a regression).

### Migration checklist

- [x] Phase 2: handler ctx semantics on shutdown (`WithoutCancel`). ✅ `a0b70f0`
- [x] Phase 2: dead-letter publish failure cap. ✅ `a0b70f0`
- [x] Phase 2: BufferedPublisher state-file documented permissions; LastSaveError surfaces persistence errors. ✅ `a0b70f0`
- [x] Phase 3: `Connection.WaitForConnection`. ✅ `a0b70f0`
- [x] Phase 3: membroker `Unsubscribe`. ✅ `a0b70f0`
- [x] Default-Retry: kept current ack-and-discard semantics; `Consumer.ConsumeOnce` already logs a loud Warn at consumer start when `Retry == nil`. Flipping the default would silently break fire-and-forget callers; the warning is the right behaviour.
- [x] xDeath retry-queue naming validation: structurally moot — `RetryQueue = b.Queue + ".retry"` is computed from the main queue with no override path, so the names cannot collide.

### Related new packages

- [new/12-infra-messaging-nats.md](../new/12-infra-messaging-nats.md) — NATS JetStream backend.
- [new/13-infra-messaging-kafka.md](../new/13-infra-messaging-kafka.md) — Kafka backend.
