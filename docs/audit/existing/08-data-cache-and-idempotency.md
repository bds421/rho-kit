# data/cache + data/idempotency — caching and idempotency stores

## Landed

- ✅ **pgstore `Unlock` owner-token check** — split-brain finding closed; `Unlock` requires `owner_token` match, migration `20260505000001` adds the column (commit `1f06b5e`).
- ✅ **Idempotency `WithTTL` rejects non-positive durations** — panics in constructor; eliminates the Redis "permanent lock on TTL=0" path (commit `36cf34b`).
- ✅ **`ComputeCache` WaitGroup race fixed** — `bgMu` serialises `bgWg.Add` against `Wait` so `Close` can't race with a refresh trigger (commit `36cf34b`).
- ✅ **`MemoryCache` default `MaxCost = 64 MiB`** instead of `math.MaxInt64`; opt-in for unbounded (commit `36cf34b`).
- ✅ **`Store` interface reshaped with token + fingerprint** — `TryLock(ctx, key, fingerprint, ttl) → (token, fingerprintMismatch, ok, err)`, `Set` requires token, `Get` reports body-mismatch (commit `1f06b5e`).
- ✅ **redisstore drops in-process `tokens` map** — token round-trips through caller; new Lua compare-then-write script in `Set` requires token+fingerprint match (commit `1f06b5e`).
- ✅ **MemoryStore body-nil semantics preserved** — defensive copy keeps nil bodies as nil through `Set` → `Get` (commit `1f06b5e`).
- ✅ **Idempotency backends reject non-positive TTL** — `ErrInvalidTTL` sentinel returned by Memory / Redis / PG `TryLock` and `Set`, closing the direct-caller path that bypassed the middleware's panic guard (commit `a01fad7`).
- ✅ **`ComputeCache` zero-TTL contract** — `ComputeFunc` returning `ttl <= 0` now errors out; documented divergence from base Cache (which treats 0 as no-expiration) because the stale-while-revalidate layer makes 0 meaningless (commit `6ba1e7d`).
- ✅ **`NewTypedCache` / `NewComputeCache` reject nil backend** at construction (commit `6ba1e7d`).
- ✅ **`pgstore.New` panics on nil `*sql.DB`** (commit `6ba1e7d`).

## Open

_Closed — see Recently Landed below._

## Recently Landed (Phase 3, commit `fd4f579`)

- ✅ **Bounded background sweeper** — `evictBudget=256` per tick, `sweepInterval=30s`, `sweepExpiredLocked(budget)` runs from `Run(ctx)`; eviction no longer holds `mu.Lock()` over O(n).
- ✅ **`BulkCache` interface** — `MGet`/`MSet`/`SetNX` exposed via capability detection; `data/cache.MGet/MSet/SetNX` package functions fall back gracefully on backends that haven't implemented BulkCache yet (with a doc-warning about the racy fallback).
- ✅ **`memory_cache` BulkCache impl** — atomic SetNX via `setNXMu`; MGet/MSet trivially implemented.
- ✅ **`rediscache` BulkCache impl** — pipelined SET-EX MSet, MGET MGet, native SET NX SetNX.
- ✅ **Compute cache surfaces backend errors** — `executeCompute` now calls `recordError + slog.Warn` on backend Set failure; `WithComputeLogger` lets callers route the warning.
- ✅ **pgstore corrupted-headers policy documented** — fail-closed is the explicit contract; comment in `Get` now records why.

### Migration checklist

- [x] Phase 2: ComputeCache zero-TTL contract (reject, or encode no-expire sentinel). ✅ `6ba1e7d`
- [x] Phase 3: MemoryStore eviction heap/sweeper. ✅ `fd4f579`
- [x] Phase 3: cache.Cache add MGet/MSet/SetNX. ✅ `fd4f579`
- [x] Phase 3: compute cache surface backend Set errors. ✅ `fd4f579`
- [x] Phase 3: pgstore Get corrupted-headers policy decision. ✅ `fd4f579`
