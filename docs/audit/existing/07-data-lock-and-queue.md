# data/lock + data/queue — distributed lock and Redis-list queue

## Landed

- ✅ **`data/lock` interface refit** — added `redislock.Locker` (per-call returned `Lock` handle) satisfying `lock.Locker`; legacy stateful `*Lock` deprecated but kept for backward compat (commit `2408d15`).
- ✅ **`Lock.Release` returns `ErrLockLost`** on token mismatch; `WithLock` joins it with fn's error so callers can `errors.Is(err, lock.ErrLockLost)` (commit `2408d15`). The `Lock` token race on shared instance is also gone — the new pattern returns a fresh handle per Acquire.
- ✅ **Per-consumer Redis queue processing list** — `{queue}:processing:{consumerID}` (UUID v7 per `NewQueue`, override via `WithConsumerID`); recovery only scans this consumer's own list (commit `f4a0a95`). Eliminates rolling-deploy double-processing.
- ✅ **ID-keyed remove from processing list** — Lua tombstone script finds entry by message ID, LSETs sentinel, LREMs sentinel; payload-equality LREM race is gone (commit `f4a0a95`).
- ✅ **Recovery silent-drop fix** — `recoverProcessing` now LRanges and feeds entries through the normal `handleMessage` flow (which removes by ID after dispatch). Previous RPop-then-dispatch dropped messages whose dispatch failed (commit `f4a0a95`).
- ✅ **redislock `tryAcquire` orphan probe** — best-effort GET on transient SET errors detects "SET landed but response failed" and treats it as success when our token is in Redis (commit `432f001`).

## Open

_Closed — see Recently Landed below._

## Recently Landed (Phase 3, commit `7e3e1d4`)

- ✅ **Bounded interleaved recovery** — `recoverProcessingBatchSize=10`; `processOnce` now runs one recovery batch every 10 BLMove iterations instead of draining the entire processing list before serving new traffic. Eliminates the head-of-line-blocking restart pattern.

### Migration checklist

- [x] Phase 2: `Acquire` surfaces transient-SET orphans (probe via `GET` on transient errors). ✅ `432f001`
- [x] Phase 2: bounded `recoverProcessing` interleaved with new-message reads. ✅ `7e3e1d4`

### Related new packages

- [new/09-data-lock-pg-advisory.md](../new/09-data-lock-pg-advisory.md) — Postgres advisory lock (recommended in redislock package doc for critical writes).
- [new/10-data-ratelimit-sliding-window.md](../new/10-data-ratelimit-sliding-window.md) — sliding window rate-limit primitive.
