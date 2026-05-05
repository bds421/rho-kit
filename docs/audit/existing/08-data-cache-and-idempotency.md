# data/cache + data/idempotency — caching and idempotency stores

### [CRITICAL] pgstore `Unlock` has no owner check (split-brain)
**File**: `data/idempotency/pgstore/store.go:165-175`
**Issue**: `DELETE FROM ... WHERE key=$1 AND response_body IS NULL` — no token/owner check. After A's TTL expires and B reacquires via the expired-row UPDATE branch, A's delayed `Unlock` removes B's still-valid lock row. C then `TryLock`s while B is still mid-flight → two concurrent in-flight processors for the same Idempotency-Key.
**Fix**: Add an owner_token column (or use the row's `expires_at` snapshot as the token). `Unlock` becomes `DELETE ... WHERE key=$1 AND response_body IS NULL AND owner_token=$2`. Migration adds the column.
**Effort**: M
**Phase**: 2
**Migration**: New column; `Store` interface change to return token from `TryLock` and accept it in `Unlock`/`Set`. Coordinate with the redisstore fix below.

### [HIGH] Idempotency `WithTTL(0)` creates permanent locks; backends disagree on zero/sub-second TTLs
**Files**: `httpx/middleware/idempotency/idempotency.go:79,132,181` + `data/idempotency/redisstore/store.go:106` + `data/idempotency/pgstore/store.go:49`
**Issue**: `WithTTL` accepts zero and negative durations. Three backends, three behaviors:
- Redis `SET NX` with `EX 0` creates a lock without expiry (the EX is ignored). Permanent lock if the consumer crashes before clearing it.
- MemoryStore treats TTL=0 as immediately expired.
- PostgreSQL rounds sub-second durations down to `"0 seconds"` interval, so anything < 1s also expires immediately.

The same TTL value produces three different operational behaviors. Misconfigured middleware can permanently brick a request key in Redis; the same config "works" in tests with MemoryStore.
**Fix**: Make `WithTTL` panic (or return an error) on non-positive values. Document the minimum-precision (1 second). Add backend tests for zero, negative, and sub-second TTLs that all agree on the rejection path.
**Effort**: S
**Phase**: 1

### [HIGH] `ComputeCache` races `bgWg.Add` with `Close`/`Wait` (WaitGroup misuse)
**File**: `data/cache/compute.go:311`
**Issue**: `triggerBackgroundRefresh` checks `closed` then calls `bgWg.Add(1)`. `Close` sets `closed` and immediately calls `bgWg.Wait()`. A concurrent refresh can call `Add` while `Wait` is active — classic `sync.WaitGroup` misuse. May panic, may let refresh work start after `Close` returns, makes shutdown nondeterministic.
**Fix**: Guard the closed-check + Add with a mutex. Or replace the WaitGroup lifecycle with a channel/errgroup that cannot accept new work after close begins.
**Effort**: S
**Phase**: 1

### [HIGH] `data/idempotency.Store` interface lacks request-fingerprint + owner-token
**File**: `data/idempotency/idempotency.go:13-25`
**Issue**: The standard Idempotency-Key pattern requires the store to detect body mismatch (same key, different body) and return 422. Interface has no place for a request hash; responsibility punted to middleware. Also no owner token plumbing → split-brain (above).
**Fix**: Reshape interface:

```go
type Store interface {
    TryLock(ctx, key string, fingerprint []byte, ttl time.Duration) (token string, fingerprintMatch bool, ok bool, err error)
    Set(ctx, key, token string, resp CachedResponse, ttl time.Duration) error
    Unlock(ctx, key, token string) error
    Get(ctx, key string, fingerprint []byte) (resp *CachedResponse, fingerprintMismatch bool, err error)
}
```

Middleware uses `fingerprintMismatch` → 422.
**Effort**: M
**Phase**: 2
**Migration**: All three implementations (memory, pg, redis) must update together. Add a `StoreV1` shim if external consumers exist.

### [HIGH] redisstore `tokens` map is process-local and overwritten on duplicate TryLock
**File**: `data/idempotency/redisstore/store.go:106-141`
**Issue**: `RedisStore.tokens` keyed only by lockKey. Concurrent same-key `TryLock` from same process overwrites the token; first goroutine's later `Unlock` reads the second's token and erroneously releases someone else's lock. Also unbounded growth without cleanup if `Unlock` never runs (e.g., handler panic).
**Fix**: Don't keep an in-process map; return token from `TryLock` and pass it back in `Unlock`/`Set`. Ties into the interface change above.
**Effort**: S (depends on interface change)

### [HIGH] redisstore `Set` doesn't require lock-token match
**File**: `data/idempotency/redisstore/store.go:91-101`
**Issue**: Plain `SET key val EX ttl` — a stale/late writer can overwrite a fresh response from another caller.
**Fix**: Lua script that writes the response only if the lock token matches (or atomically replaces lock with response). Documented prerequisite of the interface change.
**Effort**: S

### [HIGH] MemoryStore `Set` normalizes nil vs empty body inconsistently
**File**: `data/idempotency/idempotency.go:122-158`
**Issue**: Always `make([]byte, len(resp.Body))` — caller passing `Body=nil` gets a non-nil empty body back from `Get`. Middleware that distinguishes "no body" (204) from "empty body" (200) gets confused.
**Fix**: Preserve nil-ness, or normalize semantics in the type and apply consistently across all three implementations.
**Effort**: S

### [HIGH] `MemoryCache` default `MaxCost = math.MaxInt64` (effectively unbounded)
**File**: `data/cache/memory_cache.go:111`
**Issue**: `NewMemoryCache` defaults `MaxCost` to `math.MaxInt64`. Attacker-controlled or high-cardinality cache keys can grow memory until process pressure or OOM unless every caller remembers to set `WithMaxSize`/`WithMaxCost`. Same class of bug as the rate limiter's keyed map — but the rate limiter has it right (sharded LRU bound at 10k/shard) and the memory cache doesn't.
**Fix**: Set a conservative default cap (e.g., 64 MiB or 100k entries). Require explicit opt-in for unbounded caches via `WithUnboundedCost()`. Expose memory-cache sizing in the golden-path config so `app.Builder.WithProductionDefaults()` enforces it.
**Effort**: S
**Phase**: 1

### [MEDIUM] `ComputeCache` zero TTL contradicts the base cache interface
**Files**: `data/cache/cache.go:56` + `data/cache/compute.go:276`
**Issue**: Base cache interface says zero TTL means *no expiration*. `ComputeCache` stores `ExpiresAt: now + ttl` — with `ttl == 0`, the envelope is immediately stale/expired even if the backend keeps the value. Callers using normal cache semantics see immediate recompute / stale-window behavior instead of a non-expiring computed value.
**Fix**: Choose one contract. Either reject zero TTL from `ComputeFunc` (preferred — explicit), or encode a no-expiration sentinel in the compute envelope and skip the staleness check.
**Effort**: S
**Phase**: 2

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

- [ ] Phase 1: idempotency `WithTTL` reject non-positive; backend tests assert agreement.
- [ ] Phase 1: ComputeCache fix WaitGroup race (mutex around closed-check + Add).
- [ ] Phase 1: MemoryCache conservative default MaxCost; require opt-in for unbounded.
- [ ] Phase 2: reshape `Store` interface (token + fingerprint plumbing).
- [ ] Phase 2: pgstore add owner_token column + migration.
- [ ] Phase 2: redisstore drop in-process tokens map; Lua-guarded `Set`.
- [ ] Phase 2: normalize body-nil semantics across all three impls.
- [ ] Phase 2: ComputeCache zero-TTL contract (reject, or encode no-expire sentinel).
- [ ] Phase 3: MemoryStore eviction heap/sweeper.
- [ ] Phase 3: cache.Cache add MGet/MSet/SetNX.
- [ ] Phase 3: compute cache surface backend Set errors.
