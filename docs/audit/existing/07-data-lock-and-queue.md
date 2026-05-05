# data/lock + data/queue — distributed lock and Redis-list queue

The two most subtle correctness areas in the kit. Three of the eleven CRITICAL findings live here.

### [CRITICAL] `data/lock` interface and redislock implementation are incompatible
**File**: `data/lock/lock.go:13-18` + `data/lock/redislock/lock.go:117`
**Issue**: Interface declares `Locker.Acquire(ctx, key string) (Lock, bool, error)` returning a per-call `Lock` value. Redis impl exposes stateful `Lock.Acquire(ctx)` with no key, no Lock return. They cannot be assigned to each other. The stateful pattern forces callers into the dangerous "do not call Acquire twice" rule — if a caller forgets `Release` and re-Acquires, `tryAcquire` returns an error string and clears the previous token, leaving the actual Redis lock orphaned until TTL.
**Fix**: Either delete `lock.Locker` or refit redislock to it (per-call `Lock` value returned from `Acquire(ctx, key)`, with stateful key/token bound to the returned object). The per-call object pattern eliminates the re-acquire footgun.
**Effort**: M
**Phase**: 2
**Migration**: Public API change. Provide a one-release alias from the old `*Lock` to the new returned-handle pattern; document migration.

### [CRITICAL] Redis list queue uses one shared `:processing` list across consumers
**File**: `data/queue/redisqueue/queue.go:399-410` + `helpers.go:195-219`
**Issue**: `processingQ = queue + ":processing"` is shared across all consumer instances. `recoverProcessing` runs at startup of every Process call and `RPop`s items from the shared processing list — including items currently being processed by other live consumers. A rolling deploy or scale-out silently double-processes every in-flight message.
**Fix**: Per-consumer processing list (`queue + ":processing:" + consumerID`). Recovery only rescans this consumer's own list. To recover from dead consumers, add a periodic scan-and-claim using heartbeat keys per consumer.
**Effort**: L
**Phase**: 2

### [CRITICAL] `LRem`-by-data races on duplicate payloads + recovery silently drops messages
**File**: `data/queue/redisqueue/helpers.go:99-107,156-165,195-219`
**Issue**: `LRem(processingQ, 1, data)` matches by literal payload. Two messages with identical bytes (e.g. retry that re-enqueues same JSON) have the wrong copy removed. Worse: `recoverProcessing` `RPop`s the message **before** dispatching to the handler. If `handleFailedMessage`/`deadLetter` returns false (DLQ pipeline failed), the message is already gone and silently dropped.
**Fix**: Use msg.ID-keyed in-flight hash; LIndex peek before remove; atomic Lua re-queue on dispatch failure. Lands together with the per-consumer processing list above.
**Effort**: L
**Phase**: 2

### [HIGH] redislock `Acquire` regenerates token on transient SET error → orphan window
**File**: `data/lock/redislock/lock.go:117-157`
**Issue**: On transient `tryAcquire` error, `l.token = ""` is set and the error bubbles. But if the underlying SET actually reached Redis successfully (network blip on the response), the lock is held in Redis with a token the client has discarded. Next `Acquire` SETNX returns false until TTL.
**Fix**: On transient errors, optionally probe `GET key` to check ownership before discarding the token. Or document that callers must `Release` (which no-ops safely on token mismatch) before the next Acquire — and make Release surface "I lost the lock" via `ErrLockLost`.
**Effort**: S

### [HIGH] redislock `Release` never returns `ErrLockNotHeld`
**File**: `data/lock/redislock/lock.go:190-200`
**Issue**: Lua returns `1` on successful DEL, `0` on token mismatch. Go wrapper ignores the return value, returning `nil` even when the lock was already lost. Callers needing "I lost the lock during my critical section" cannot detect it. `WithLock` masks the lost-lock as success.
**Fix**: Return `ErrLockLost` when script result is 0. Update `WithLock` to surface it.
**Effort**: S

### [HIGH] Queue `recoverProcessing` runs unbounded at startup → head-of-line blocking
**File**: `data/queue/redisqueue/queue.go:413-417` + `helpers.go:195-219`
**Issue**: `processOnce` calls `recoverProcessing` synchronously before any new `BLMOVE`. If thousands of messages are stuck, recovery runs them one-at-a-time before any new traffic flows. Combined with the silent-drop bug above this gets worse.
**Fix**: Bound recovery per restart (mirror `maxPendingPerRestart` from the stream consumer); interleave with new-message reads; ensure failed dispatch re-queues to processingQ.
**Effort**: M

### [HIGH] redislock token race on shared `Lock` instance
**File**: `data/lock/redislock/lock.go:81-86` (doc comment) + Acquire/Release
**Issue**: `l.token` not mutex-protected. Doc says "by design — use separate Lock instances for concurrent goroutines". But the obvious mental model treats `Lock` as a reusable resource. Combined with the interface-mismatch fix above, the per-call returned-Lock pattern eliminates this entirely.
**Fix**: Land with the interface change (no separate fix).

### Migration checklist

- [ ] Phase 2: redislock interface refit; per-call returned `Lock` handle; deprecate stateful `*Lock`.
- [ ] Phase 2: `Release` returns `ErrLockLost`; `WithLock` propagates it; `Acquire` surfaces transient-SET orphans.
- [ ] Phase 2: per-consumer processing list + ID-keyed in-flight hash + Lua atomic re-queue.
- [ ] Phase 2: bounded `recoverProcessing` interleaved with new-message reads.

### Related new packages

- [new/09-data-lock-pg-advisory.md](../new/09-data-lock-pg-advisory.md) — Postgres advisory lock (recommended in redislock package doc for critical writes).
- [new/10-data-ratelimit-sliding-window.md](../new/10-data-ratelimit-sliding-window.md) — sliding window rate-limit primitive.
