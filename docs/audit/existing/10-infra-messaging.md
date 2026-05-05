# infra/messaging — AMQP backend, buffered publisher, debug HTTP, membroker

## Landed

- ✅ **AMQP publisher `mandatory=true` + `NotifyReturn`** — unroutable messages now surface as `ErrUnroutable` to the caller instead of being silently ack'd (commit `068eeb5`).
- ✅ **`debughttp` Guard middleware** — refuses to register in production env, requires an injected `Authenticator` (BasicAuth or AllowFromHeader, both with constant-time compare) (commit `068eeb5`).
- ✅ **Topology rejects sub-millisecond `Retry.Delay`** — eliminates the "TTL truncates to 0 → tight redelivery loop" bug (commit `068eeb5`).

## Open

### [HIGH] AMQP consumer detaches handler ctx during shutdown — handlers can't see it
**File**: `infra/messaging/amqpbackend/consumer.go:174-182`
**Issue**: When `ctx.Err() != nil` during shutdown, handler is invoked with `context.WithTimeout(context.Background(), handlerShutdownTimeout)`. This **detaches** from parent ctx; the comment claims handlers should "check `ctx.Err()`" but it'll always be nil for the new ctx until the 30s deadline. Handlers performing slow I/O during shutdown have no early-bail signal; they block shutdown for the full 30s.
**Fix**: Use `context.WithoutCancel(parent) + WithTimeout` (Go 1.21+), or pass an `isShutdown bool` flag, or expose a sentinel value handlers can read. Correct the misleading comment.
**Effort**: S
**Phase**: 2

### [HIGH] Dead-letter publish failure → bounce up to MaxRetries × 3
**File**: `infra/messaging/amqpbackend/consumer.go:262-291`
**Issue**: When the dead-exchange publish fails, `Nack(false, false)` routes the message back through the retry DLX. With a permanently-failing dead exchange (e.g. typo), the message bounces `MaxRetries × 3` (`safetyMaxBounceMultiplier`). With MaxRetries=10 and 30s retry TTL = ~15min of CPU/network thrash per stuck message. Each bounce re-runs the failing handler.
**Fix**: After N consecutive dead-publish failures, force-discard (or move to a local file). Consider a second confirm channel reserved for dead-letter publishes; treat sustained DLE failures as a fatal config error.
**Effort**: M
**Phase**: 2

### [HIGH] BufferedPublisher final-drain loses messages on shutdown without state file
**File**: `infra/messaging/buffered_publisher.go:291-316,386-394`
**Issue**: 15s `finalDrain`. Messages still pending are "logged and lost" unless `WithBufferedStateFile` was set. `saveLocked` errors only logged → disk-full during drain leaves messages in neither buffer nor disk.
**Fix**: Make state file mandatory in production (refuse to construct without one when env != dev), or default to a temp-dir state file. Surface persistence errors to `Publish` callers (back-pressure), don't just log.
**Effort**: M

### [HIGH] BufferedPublisher state file is plaintext (PII at rest)
**File**: `infra/messaging/buffered_publisher.go:386-394` + load() at 406
**Issue**: Pending messages including full payloads persisted as JSON on disk. No encryption, no documented permission constraint.
**Fix**: Set restrictive umask via `atomicfile` (0o600) and document. Optionally wrap with the existing `crypto/encrypt` for sensitive workloads.
**Effort**: S

### [MEDIUM] AMQP `Connection.Channel` doesn't wait for in-progress reconnect
**File**: `infra/messaging/amqpbackend/connection.go:131-146`
**Issue**: Returns error when `c.conn == nil || c.conn.IsClosed()`. Caller doesn't retry; reconnection in progress is not awaited. Outbox `Relay` marks it as a publish error and increments attempts → burns retry budget on transient connection cycles.
**Fix**: Block briefly on `c.connected` (small timeout) for in-progress reconnects, or expose `WaitForConnection(ctx)` so the relay can pause poll instead of incrementing attempts.

### [MEDIUM] `actionDiscard` silently acks first failure when no retry configured
**File**: `infra/messaging/amqpbackend/consumer.go:307-321`
**Issue**: `Retry == nil` → ANY handler error → ack and discard. Opposite the AGENTS.md anti-pattern guidance ("never ACK on transient errors"). Configuration drift (forgetting `Retry`) is silently destructive.
**Fix**: Default `Retry` to a sane policy (3 retries / 10s) when nil; require explicit `WithoutRetry()` opt-in to get drop-on-error behavior; log loudly at consumer start when a binding has no retry.

### [MEDIUM] `xDeathCount` only reads `reason=rejected` — couples to retry-queue naming
**File**: `infra/messaging/amqpbackend/xdeath.go:12-49`
**Issue**: Filters by `reason=rejected`. Works as long as retry-queue naming convention isn't violated. Operator override that names retry queue identically to main queue could double-count.
**Fix**: `ValidateBindingSpecs` should require retry queue name differ from main queue name; document the dependency.

### [MEDIUM] BufferedPublisher: `directInFlight` reservation underflows when `maxSize=1`
**File**: `infra/messaging/buffered_publisher.go:222-237`
**Issue**: With `maxSize=1` and `directInFlight=true`, `effectiveMax=0` → second message rejected during normal operation.
**Fix**: Validate `maxSize >= 2` in `WithBufferedMaxSize`, or skip the reservation when `maxSize == 1`.

### [LOW] membroker `Subscribe` cannot be unsubscribed → tests leak handlers
**File**: `infra/messaging/membroker/membroker.go:56-64`
**Issue**: No `Unsubscribe`. Tests calling `Subscribe` in a loop without `Reset()` accumulate stale handlers, masking bugs.
**Fix**: Add `Unsubscribe(handlerID)` or document that callers must `Reset()` between scenarios.

### Migration checklist

- [ ] Phase 2: handler ctx semantics on shutdown (`WithoutCancel`).
- [ ] Phase 2: dead-letter publish failure cap; consider reserved DLE channel.
- [ ] Phase 2: BufferedPublisher state-file mandatory in prod; surface persistence errors to caller; restrictive umask.
- [ ] Phase 3: `Connection.WaitForConnection`; default-Retry; `xDeathCount` retry-queue-name validation; membroker `Unsubscribe`.

### Related new packages

- [new/12-infra-messaging-nats.md](../new/12-infra-messaging-nats.md) — NATS JetStream backend.
- [new/13-infra-messaging-kafka.md](../new/13-infra-messaging-kafka.md) — Kafka backend.
