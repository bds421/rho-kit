# Code review: Data interfaces B (stage 1 — unverified findings)

## Scope

- **Directories**: data/idempotency/, data/lock/, data/queue/, data/ratelimit/, data/saga/, data/stream/
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 15 (lenses inferred: correctness, design, security; expected lens count: 3)
- Status: raw reviewer findings; adversarial verification (stage 2) pending.

## Summary

| Severity | Count |
|---|---|
| CRITICAL | 0 |
| HIGH | 0 |
| MEDIUM | 1 |
| LOW | 16 |
| **Total (deduplicated)** | **26** |

**Reviewer impressions:**

> This scope is high quality: interfaces are precisely documented with explicit return contracts, invalid inputs fail closed with typed errors, and the tricky concurrency work (double-checked expiry deletion, budget-bounded sweeps, weak-pointer sweeper goroutines that cannot outlive their limiter, ctx re-checks after lock acquisition) is done carefully and explained inline. The conformance harnesses in locktest/idempotencytest pin cross-backend semantics well, including mutual-exclusion high-water-mark checks. The findings are edge cases — a narrow check-then-act window in MemoryStore.Set, a float-to-int overflow the tokenbucket constructor doesn't validate, and a few contract rough edges — rather than systemic problems.

> This scope is high quality for a security lens: keys are length- and charset-validated with typed errors, lock tokens come from crypto/rand, log statements consistently redact runtime values via core/redact, cached responses are validated fail-closed on both write and read, and the tenant wrapper uses a collision-proof length-prefixed key format. The real weaknesses are at contract boundaries rather than in the code itself — the conformance suites do not pin the most security-critical cross-backend properties (TTL-takeover lock safety, invalid-character key rejection), the stream and lock core packages lack the shared validation helpers their siblings provide, and the tenant wrapper's key-length composition can fail closed for inputs each layer individually accepts. Nothing exploitable or data-corrupting was found.

> The core interface packages in this scope are small, unusually well-documented, and show serious attention to cross-backend contract enforcement (typed sentinels, conformance suites in locktest/idempotencytest, TTL and fingerprint edge cases pinned by tests). The main systemic weakness is interface/implementation drift: the stream and queue packages advertise pluggable Producer/Consumer abstractions that their own backends do not actually implement, and validation conventions (key validators, error wrapping, sweep budgeting) are inconsistently replicated across sibling packages rather than shared. The MemoryStore and both in-memory limiters are solid, with only narrow boundary/race edge cases and minor duplication remaining.

> This scope is unusually high quality: interfaces carry precise, contract-style documentation (return tuples, TTL semantics, fingerprint rules), inputs are validated defensively at every boundary, the MemoryStore uses budgeted sweeps and double-checked expiry deletes correctly, and the conformance harnesses pin subtle cross-backend semantics (nil-vs-empty fingerprints, stale-token behavior, mutual exclusion high-water marks). The issues found are edge cases rather than systemic flaws — the most substantive being a token-leak interaction with x/time/rate's CancelAt on the tokenbucket deny path and a narrow time-of-check race in MemoryStore.Set that can silently drop a stored fingerprint. lock, queue, ratelimit, and stream core packages are interface-only and essentially clean.

> This scope is exceptionally well-hardened for security: every interface package that takes caller input (idempotency, ratelimit, queue) ships shared validators rejecting empty/oversized/control-byte/non-UTF-8 keys; lock tokens come from crypto/rand; logs go through core/redact (length-only redaction, no PII); the tenant wrapper uses collision-proof length-prefixed keys and fails closed without a tenant ID; and cached-response validation blocks header/CRLF injection on replay. Contracts are documented with unusual rigor (explicit fail-closed return contracts, ASVS annotations, conformance harnesses pinning backend behavior). The findings are edge-case and consistency issues — a tenant-wrapper key-length mismatch, non-constant-time secret comparisons in the reference store, and the lock/stream packages missing the shared validation helpers their siblings have — with nothing exploitable at HIGH or CRITICAL severity.

> This scope is unusually high quality for interface/core packages: exhaustive godoc that documents contracts and historical failure modes, typed sentinels with errors.Is discipline, defensive validation (keys, TTLs, cached responses), deterministic clock injection, bounded eviction in the MemoryStore, and strong conformance harnesses (idempotencytest, locktest) with thorough unit coverage. The idempotency and ratelimit families are mature; the main weaknesses are at the edges of the family: the queue and stream core packages export interfaces that nothing in the repo implements while their doc.go files claim otherwise, the lock package uniquely lacks the centralized key-validation pattern its siblings established, and gcra retains the single-global-mutex design that tokenbucket was explicitly refactored away from.

> This scope is unusually high-quality: the interface contracts are precisely documented (return tuples, TTL semantics, fingerprint nil-vs-empty), validation is defensive and fail-closed, and the concurrency-sensitive code (MemoryStore double-checked expiry deletes, budgeted sweeps, weak.Pointer-backed sweeper goroutines with idempotent Close) shows deliberate, well-commented design with conformance batteries pinning cross-backend behavior. The findings are all edge cases — a time-based check-then-act inside MemoryStore.Set, a float-to-int overflow guard gap in tokenbucket, and interface-shape gaps (error-less Consume) — rather than mainstream logic or race defects; nothing rose above MEDIUM.

> This is unusually security-conscious interface code: fail-closed typed errors, crypto/rand lock tokens, length-redacted logging of keys, explicit input caps with rationale comments, and conformance harnesses that pin cross-backend contracts (including subtle cases like non-nil empty fingerprints and TTL=0 divergence). The weaknesses are consistency gaps rather than outright vulnerabilities: the stream and lock cores lack the shared validation contract their siblings define, and the tenant wrapper's key namespace is forgeable when a bare store shares the backend. No injection, crypto misuse, or secret-leak issues were found in scope.

> This scope is well above average: contracts are documented in unusual detail (return-tuple semantics, TTL edge cases, boundary rationale), inputs are validated defensively and consistently, conformance suites pin cross-backend behavior, and past performance lessons (bounded eviction, weak-pointer sweepers) are recorded in comments. The main weaknesses are aspirational/dead exported interfaces in the stream and queue core packages that no backend actually implements, and the two in-memory rate limiters not applying the bounded-sweep discipline the idempotency MemoryStore already adopted. Remaining findings are polish-level: boundary-condition inconsistency, zero-value footguns, and small duplication.

> This scope is almost entirely interface definitions plus in-memory reference implementations, and it is high quality: keys/names/payloads are validated for length, UTF-8, control, and whitespace bytes (blocking log/protocol injection), cached-response headers reject control characters (blocking response splitting on replay), tokens come from crypto/rand, the tenant wrapper fails closed when no tenant is on the context, and rate limiters deny on error/degenerate config. I found no CRITICAL/HIGH security issues; the only note is a non-constant-time token comparison in the test-only MemoryStore, which is low risk given that store's documented scope.

> This scope is high quality: most files are clean interface/validation definitions, and the two real implementations (idempotency MemoryStore and the gcra/tokenbucket limiters) are unusually defensive, well-documented, and correct on the core paths. The GCRA burst math, the token-bucket ReserveN/CancelAt rollback, the weak.Pointer sweeper lifecycle, and the MemoryStore lock/expiry checks all hold up under close reading. The only issues found are minor concurrency/robustness edge cases with narrow real-world impact.

> This scope is high quality: the interfaces are unusually well-documented, the tricky logic (GCRA emission-interval math, token-bucket weak.Pointer sweepers, idempotency lock/fingerprint state machine) is carefully reasoned and well-tested, and misuse-resistance is a clear design goal in idempotency and ratelimit (typed errors, always-on validation helpers, fail-closed receivers). The weaknesses are consistency rather than correctness: the stream and lock interface packages are under-specified relative to their queue/idempotency/ratelimit siblings, and Consumer.Consume swallows fatal errors by returning nothing, which conflicts with the kit's own Run(ctx) error lifecycle convention.

> This family of interface/core packages is high quality and clearly security-conscious: lock tokens use crypto/rand (128-bit), all keys and error values are redacted before logging, replayed HTTP header names/values are validated to block CRLF/control-char injection, tenant scoping uses collision-resistant length-prefixed keys with required-tenant fail-closed semantics, and the rate limiters fail closed on invalid config or cancelled context. I found no injection, authz-bypass, or PII-leak issues; only two low-severity notes around a non-constant-time token comparison in the test-only MemoryStore and an asymmetric (nil-side) fingerprint-mismatch guard.

> This family is unusually well-documented and thoughtfully designed for misuse resistance: the interface contracts (return-tuple semantics, ErrInvalidTTL rationale, nil-vs-empty fingerprint handling, weak.Pointer sweepers) are spelled out carefully and backed by solid cross-backend conformance harnesses. The strongest issues are consistency gaps rather than outright bugs — stream and lock lack the shared validation/size-cap helpers that idempotency, ratelimit, and queue all provide, and the Consumer interfaces swallow fatal errors — plus a few minor edge-case/duplication/performance smells in the in-memory idempotency store. No CRITICAL/HIGH defects were provable in the interface-and-memory-impl code within scope.

> This scope is high-quality, defensively written Go: interfaces (lock, queue, stream, ratelimit) are thin and correct, the GCRA and token-bucket limiters implement their algorithms correctly (burst/TAT accounting, retryAfter rounding, and overflow bounds all check out), and the weak.Pointer sweeper design cleanly avoids goroutine leaks on a forgotten Close. Concurrency in MemoryStore is carefully reasoned (immutable-after-insert slices read lock-free, independent eviction budgets for items vs locks, token-ownership checks that fail closed with ErrLockLost). I found no CRITICAL or HIGH defects; the remaining items are narrow races and lifecycle/documentation edges, mostly confined to the explicitly test-only MemoryStore.

## Findings

### [MEDIUM] Tenant key namespace is forgeable by raw keys on a bare store sharing the same backend

- **Where**: `data/idempotency/tenant/tenant.go:74`
- **Dimension**: security
- **Status**: PARTIAL — package doc now forbids sharing a backend keyspace with a bare store; cryptographic unforgeability deferred to v3 (`V3_BREAKING_PROPOSALS.md`).
- **Detail**: scopedKey builds keys via coretenant.KeyFor, producing e.g. "tenant:1:a:3:foo" for tenant "a" + raw key "foo". That exact string is itself a valid raw idempotency key. A deployment that wires the tenant-wrapped store and a bare store to the same backend keyspace lets any caller who controls a raw key on the bare path address tenant-scoped rows.
- **Suggestion**: v3 — reject raw keys matching the canonical tenant-key shape in ValidateKey (with an internal pre-scoped path), or HMAC/hash scoped keys.


### [LOW] MemoryStore.Get can return a stale miss while a fresh entry exists (check-then-act race)

- **Where**: `data/idempotency/idempotency.go:371`
- **Dimension**: concurrency
- **Detail**: Get reads the entry under RLock (line 364), releases it, then on the expired path re-acquires the write lock and re-checks expiry (line 371). If a concurrent Set replaces the expired entry with a fresh one in the window between RUnlock (365) and the write-lock recheck (370-371), the recheck sees the fresh entry (not expired), skips the delete, but still returns (nil, false, nil) — a MISS — despite a valid cached response now being present. The caller then proceeds to TryLock, which sees the fresh entry and returns the contended signal, so a naive middleware could surface a spurious 409 instead of replaying the cached 2xx. Narrow race and self-healing, and this is the explicitly test-only MemoryStore, so impact is limited.
- **Suggestion**: On the recheck, if the current entry is not expired, return cloneResponse of it (after fingerprint/validation checks) instead of unconditionally returning a miss.

### [LOW] MemoryStore lazy-eviction scans 256 entries under the write lock on every Set once the soft cap is crossed

- **Where**: `data/idempotency/idempotency.go:453`
- **Dimension**: performance
- **Detail**: Set triggers sweepExpiredLocked(evictBudget=256) when `len(m.items) >= memoryStoreMaxEntries` (10_000). Because the sweep only reclaims *expired* entries (documented as not a hard cap), a working set of live long-TTL keys stays >= 10_000, so this branch fires on every single Set thereafter, walking up to 256 map entries while holding the exclusive m.mu.Lock and reclaiming nothing. That is sustained per-write CPU + lock-hold-time overhead added to every writer and every concurrent reader, indefinitely, precisely in the high-cardinality scenario the cap was meant to protect. The periodic setCount%evictInterval path already bounds the amortized cost; the len-based trigger converts it into a per-call cost with no memory benefit when entries are live.
- **Suggestion**: Drop the `len >= memoryStoreMaxEntries` unconditional trigger (rely on the periodic evictInterval + Run sweeper), or gate it so it only fires when a prior pass actually reclaimed entries, so a live-heavy working set doesn't pay a fruitless 256-scan under the lock on every write.

### [LOW] Conformance suite never tests ErrKeyInvalidChars (control bytes / whitespace / invalid UTF-8 keys)

- **Where**: `data/idempotency/idempotencytest/conformance.go:32`
- **Dimension**: test-gap
- **Detail**: The core package defines ErrKeyInvalidChars specifically because such keys 'can corrupt logs, UTF-8 sinks, or backend protocol framing' (idempotency.go:53-55), and ValidateKey enforces it. But the conformance battery only pins empty-key (ErrKeyEmpty) and oversized-key (ErrKeyTooLong) rejection. A backend that implements its own key handling without calling ValidateKey would pass the full suite while accepting keys containing \r\n or NUL bytes into Redis commands, SQL text columns, and slog output — exactly the injection class the error exists to prevent.
- **Suggestion**: Add a testRejectsInvalidKeyChars case asserting all four Store methods return idempotency.ErrKeyInvalidChars for keys containing control bytes, whitespace, and invalid UTF-8.

### [LOW] Tenant prefixing can push otherwise-valid keys past MaxKeyLen, failing every operation with ErrKeyTooLong

- **Where**: `data/idempotency/tenant/tenant.go:78`
- **Dimension**: bug
- **Detail**: scopedKey first validates the raw key against idempotency.MaxKeyLen (256 bytes), then prepends the tenant ID via coretenant.KeyFor (tenant IDs are themselves allowed up to core/tenant.MaxIDLen = 256 bytes), then re-validates the combined key against the same 256-byte cap. A raw key that passes idempotency.ValidateKey (e.g. 200 bytes) combined with a 100-byte tenant ID exceeds the cap, so Get/TryLock/Set/Unlock all return ErrKeyTooLong for that tenant while working for tenants with shorter IDs. It fails closed, but the effective key budget silently varies per tenant and the wrapper breaks the Store contract ("keys up to MaxKeyLen are accepted") in a data-dependent way that only surfaces in production for the unlucky tenant.
- **Suggestion**: Either hash the scoped key (as the HTTP middleware does for client keys) so the stored key has fixed length, or document the reduced effective raw-key budget and validate it up front (raw key <= MaxKeyLen - prefix overhead) with a distinct, explanatory error.

### [LOW] lock package defines no shared key-validation contract, unlike every sibling data package, forcing backends to reinvent it

- **Where**: `data/lock/lock.go:36`
- **Dimension**: api-design
- **Detail**: idempotency, ratelimit, and queue each centralize key/name validation (ValidateKey/ValidateName plus MaxKeyLen constants) in the core interface package so all backends agree. The lock package exposes only Locker/Lock with no ValidateKey, no max-length constant, and no documented Acquire behavior for empty or malformed keys. Backends consequently reinvent it: redislock defines its own validateLockKey and MaxLockKeyLen (redislock/lock.go:112-126) while pgadvisory's Acquire has no equivalent named validator — so key acceptance rules can silently diverge across Locker implementations that callers expect to be interchangeable.
- **Suggestion**: Add lock.ValidateKey and a MaxKeyLen constant to the core package, document the Acquire contract for invalid keys, and have backends delegate to it (mirroring ratelimit).

### [LOW] No shared lock-key validation helper; each backend reimplements it privately

- **Where**: `data/lock/lock.go:40`
- **Dimension**: api-design
- **Detail**: Locker.Acquire takes an arbitrary key string with no documented shape contract and no exported ValidateKey in the core package. Both in-tree backends carry duplicate private validateLockKey functions (data/lock/redislock/lock.go:119, data/lock/pgadvisory/pgadvisory.go:63) enforcing the same hygiene, and the locktest conformance suite does not pin it either (see separate finding). Third-party Locker implementations therefore have no guardrail against empty, oversized, or control-byte keys reaching Redis command framing or logs, and the two in-tree copies can drift.
- **Suggestion**: Export lock.ValidateKey (mirroring ratelimit.ValidateKey) from the core package, delegate both backends to it, and state in the Locker.Acquire doc that implementations must reject invalid keys.

### [LOW] ValidateMessage treats maxPayloadBytes=0 (the zero value) as 'disable the payload cap'

- **Where**: `data/queue/queue.go:87`
- **Dimension**: api-design
- **Detail**: ValidateMessage(msg, 0) silently disables the payload size check (line 106 only applies the cap when maxPayloadBytes > 0), while negative values return an error. Because 0 is the natural Go zero value of an unset config field, a caller that forgets to wire a limit gets unbounded payloads instead of the advertised DefaultMaxPayloadBytes safety default — the opposite of the fail-closed posture the rest of this package (and MaxCachedBodyBytes in idempotency) takes. The magic sentinel is documented only in the function comment.
- **Suggestion**: Make 0 mean DefaultMaxPayloadBytes and require an explicit sentinel (e.g. -1 or a NoPayloadLimit constant) to disable the cap.

### [LOW] Payload-size cap in ValidateMessage is opt-in (0 disables), an easy footgun

- **Where**: `data/queue/queue.go:106`
- **Dimension**: api-design
- **Detail**: ValidateMessage enforces the payload cap only when `maxPayloadBytes > 0`; passing 0 silently disables the largest safety check, and a backend that forgets to thread its limit (or reads an unset config as 0) will accept unbounded payloads with no error. This contrasts with idempotency.ValidateCachedResponse, where MaxCachedBodyBytes is always enforced and cannot be turned off by a caller mistake. Given DefaultMaxPayloadBytes exists precisely to bound this, the safer-by-construction choice would default to it rather than to unbounded.
- **Suggestion**: Treat non-positive as DefaultMaxPayloadBytes, or require an explicit sentinel (e.g. a negative or a distinct 'Unlimited' constant) to disable the cap, so a plain zero cannot silently remove the protection.

### [LOW] Consumer.Consume returns no error, forcing implementations to swallow fatal backend failures

- **Where**: `data/queue/queue.go:136`
- **Dimension**: error-handling
- **Detail**: Both queue.Consumer.Consume (data/queue/queue.go:136) and stream.Consumer.Consume (data/stream/stream.go:30) are declared as blocking methods with no return value: "blocks and processes messages until ctx is cancelled". An implementation whose backend connection dies permanently (auth revoked, stream deleted, network partition beyond retry budget) has no channel to report that terminal failure — it can only log and return, and the caller cannot distinguish a clean ctx-cancel exit from a consumer that silently died, so a service keeps running while consuming nothing. Every other blocking loop in this scope (e.g. idempotency MemoryStore.Run) returns error precisely so lifecycle runners can detect abnormal exits.
- **Suggestion**: Change the interface to Consume(ctx, queue, handler) error (returning nil on ctx cancellation, non-nil on terminal backend failure), matching the MemoryStore.Run convention and typical lifecycle-runner wiring.

### [LOW] Per-key limiter maps have no cardinality cap; attacker-controlled keys grow memory unbounded between sweeps

- **Where**: `data/ratelimit/gcra/gcra.go:236`
- **Dimension**: performance
- **Detail**: gcra stores one TAT entry per key (line 236) and tokenbucket creates a bucket on every Allow — including denied ones — with eviction only via the periodic sweeper (default 5 minutes). If the key is derived from attacker-controllable input (e.g. spoofable X-Forwarded-For or arbitrary user identifiers), a flood of unique 256-byte keys accumulates for a full sweep interval: at 10k req/s that is ~3M live entries (~1 GiB) before the first eviction pass, and WithoutSweeper removes even that bound. Unlike idempotency.MemoryStore (memoryStoreMaxEntries forces inline eviction), neither limiter has an inline size threshold or hard cap. The package doc acknowledges unbounded growth without a sweeper but not the within-interval exposure.
- **Suggestion**: Add an inline eviction trigger at a size threshold (like memoryStoreMaxEntries) or a hard cardinality cap with a documented overflow policy, in both gcra and tokenbucket.

### [LOW] Limiter.Allow contract cites token-bucket as an example of retryAfter==0 on denial, but the kit's tokenbucket returns a real retryAfter

- **Where**: `data/ratelimit/ratelimit.go:54`
- **Dimension**: api-design
- **Detail**: The interface doc says zero retryAfter means 'the limiter has no opinion (e.g. token-bucket implementations that only know denied)', yet data/ratelimit/tokenbucket computes and returns the projected refill delay on every denial (tokenbucket.go:292-300, pinned by TestRetryAfter_AccurateWhenDenied). The stale example misleads implementers of new Limiter backends about what callers (e.g. Retry-After header middleware) can expect, and misleads callers into treating tokenbucket denials as opinion-less.
- **Suggestion**: Reword the example (e.g. 'implementations that cannot compute a wait') or drop it; the kit no longer ships a limiter that returns 0 on denial.

### [LOW] ValidateKey error reporting is internally inconsistent and diverges from sibling packages; the invalid-rune helper is triplicated

- **Where**: `data/ratelimit/ratelimit.go:60`
- **Dimension**: api-design
- **Detail**: ValidateKey returns the bare ErrInvalidKey sentinel for empty and invalid-character keys but a wrapped, detailed error only for the too-long case, so callers and logs cannot distinguish "empty" from "contains control bytes". The sibling idempotency package exposes distinct sentinels (ErrKeyEmpty/ErrKeyTooLong/ErrKeyInvalidChars) and queue wraps every branch with a descriptive message — three different conventions for the same concept within one module. The containsInvalidKeyRune/containsInvalidStringBytes helper is also copy-pasted identically in data/ratelimit/ratelimit.go:73, data/idempotency/idempotency.go:78, and data/queue/queue.go:112, all inside the same data module.
- **Suggestion**: Wrap each ValidateKey branch with a descriptive %w message (or distinct sentinels matching idempotency), and hoist the shared rune check into an internal package within the data module.

### [LOW] Sweeper/Allow bucket-deletion race can admit more than one extra request per cycle

- **Where**: `data/ratelimit/tokenbucket/tokenbucket.go:259`
- **Dimension**: concurrency
- **Detail**: The comment (lines 253-259) claims 'at most one extra admission per sweep-cycle-per-key.' In fact, when the sweeper deletes a full bucket (line 226), any number of goroutines that already passed the map lookup (lines 277-282) still hold the old *bucket and can drain up to its full capacity after unlock, while goroutines arriving after deletion create a fresh full bucket and drain up to capacity again — so a burst of up to ~capacity extra admissions is possible, not one. Impact is negligible for correctness/security because it only occurs when the bucket is already full (i.e. the key is not being rate-limited), but the code comment materially understates the bound.
- **Suggestion**: Correct the comment to reflect the real (bounded-by-capacity, only-at-full-bucket) over-admission, or have the sweeper hold the map lock semantics such that a bucket referenced by an in-flight Allow is not reclaimed.

### [LOW] now snapshot taken before lock lets a later goroutine move rate.Limiter.last backward

- **Where**: `data/ratelimit/tokenbucket/tokenbucket.go:270`
- **Dimension**: concurrency
- **Detail**: Allow captures now := l.now() at line 270, then releases l.mu at line 282 and only later calls b.lim.ReserveN(now, 1) at line 284. Two goroutines contending on the same key can capture now out of order relative to their ReserveN calls: if goroutine B (now=T+d) reserves first and goroutine A (now=T) reserves second, x/time/rate's advance() clamps to the earlier time and reserveN sets lim.last=T, moving the bucket's internal clock backward by d. The next caller then computes elapsed from the earlier last and accrues extra tokens, i.e. minor over-admission bounded by refill*d (d = scheduling gap between snapshot and reservation). Effect is small but it is a real time-ordering race not covered by the documented 'sweeper race' comment.
- **Suggestion**: Capture now immediately before ReserveN (after the map lookup) rather than at the top of Allow, so the timestamp handed to the per-key limiter reflects the actual reservation order.

### [LOW] stream core package has no validation helpers or input bounds, unlike every sibling data package

- **Where**: `data/stream/stream.go:24`
- **Dimension**: api-design
- **Detail**: queue exports ValidateName/ValidateMessage with MaxNameBytes/DefaultMaxPayloadBytes/MaxBatchMessages; ratelimit and idempotency export ValidateKey with length and charset rules. The stream package (31 lines) defines Producer.Produce(ctx, stream string, payload map[string]string) with no shared validation at all: no stream-name length/charset rule, no cap on payload field count, field-name bytes, or value bytes. redisstream compensates with its own redis.ValidateName and a package-local ValidateMessage, so today's backend is safe — but the contract itself imposes nothing, and any new Producer/Consumer implementation silently accepts control-byte stream names (log/protocol framing corruption) and unbounded payload maps (memory/backend abuse). The kit's own pattern is that the core package owns the cross-backend validation contract.
- **Suggestion**: Add stream.ValidateName and stream.ValidatePayload (field-count, name/value byte caps, UTF-8/control-byte rules) mirroring the queue package, and reference them in the Producer/Consumer doc contracts.

### [LOW] stream.Consumer.Consume also lacks an error return (same defect as queue.Consumer)

- **Where**: `data/stream/stream.go:30`
- **Dimension**: api-design
- **Detail**: Same issue as data/queue/queue.go:136: Consume blocks until ctx is cancelled and returns nothing, so a redisstream consumer that permanently loses its connection or whose consumer group is deleted has no channel to report a fatal error; the caller cannot distinguish clean shutdown from silent failure.
- **Suggestion**: Add an error return in lockstep with the queue.Consumer change.

