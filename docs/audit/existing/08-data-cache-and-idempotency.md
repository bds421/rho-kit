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

### [MEDIUM] MemoryStore eviction is O(n) under write lock
**File**: `data/idempotency/idempotency.go:122-141`
**Issue**: Every 100 Sets (or when len ≥ 10000), eviction scans every map entry while holding `mu.Lock()`. With 10k entries and write-heavy load, regularly stalls all readers/writers. Insertion proceeds even if every entry is unexpired → map grows past the cap between Sets.
**Fix**: Heap of expirations or background sweeper goroutine; enforce hard max with FIFO/LRU eviction when cap is hit.

### [MEDIUM] `data/cache.Cache` interface lacks MGet/MSet/SetNX
**File**: `data/cache/cache.go:51-65` + `data/cache/rediscache/cache.go`
**Issue**: Interface only Get/Set/Delete/Exists. Bulk operations require N round-trips. No `SetNX` — can't implement cross-process compute-once at the cache layer.
**Fix**: Add `MGet([]string)`, `MSet(map[string][]byte, ttl)`, `SetNX(key, val, ttl) (bool, error)`. Redis impl is one MGET/MSET/SET NX call; memory impl is trivial.
**Effort**: S

### [MEDIUM] `executeCompute` swallows backend Set errors silently
**File**: `data/cache/compute.go:300-305`
**Issue**: Backend Set failure (Redis OOM, exceeds maxValueSize, network) returns `(val, nil)` with no error counter incremented. Operators see no signal that compute cache stopped persisting.
**Fix**: Call `cc.recordError()` on backend Set failure; emit a debug log including key prefix.

### [MEDIUM] pgstore `Get` fails closed on corrupted headers JSON
**File**: `data/idempotency/pgstore/store.go:90-101`
**Issue**: Corrupted headers JSON returns an error → middleware treats as "key not found" → re-processes the request. Acceptable if intentional; document the policy.
**Fix**: Decide explicitly: fail closed (current) and document, or partially recover (return body/status with empty headers + log).

### Migration checklist

- [x] Phase 2: ComputeCache zero-TTL contract (reject, or encode no-expire sentinel). ✅ `6ba1e7d`
- [ ] Phase 3: MemoryStore eviction heap/sweeper.
- [ ] Phase 3: cache.Cache add MGet/MSet/SetNX.
- [ ] Phase 3: compute cache surface backend Set errors.
- [ ] Phase 3: pgstore Get corrupted-headers policy decision.
