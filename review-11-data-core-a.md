# Code review: Data interfaces A (stage 1 — unverified findings)

## Scope

- **Directories**: data/actionlog/, data/apikey/, data/approval/, data/budget/, data/cache/, data/cron/, data/tenant/
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 14 (lenses inferred: correctness, design, security; expected lens count: 3)
- Status: raw reviewer findings; adversarial verification (stage 2) pending.

## Summary

| Severity | Count |
|---|---|
| CRITICAL | 0 |
| HIGH | 0 |
| MEDIUM | 1 |
| LOW | 17 |
| **Total (deduplicated)** | **18** |

**Reviewer impressions:**

> This scope is unusually high quality for a security review: pervasive input validation with schema-mirrored length caps, HMAC-signed entries and cursors with constant-time comparison, explicit tenant-scoping contracts (Query.Validate, approval.TenantStore), fail-closed defaults, and honest in-code documentation of known limitations (TOCTOU L057, singleflight context detachment). The real issues found are design-level gaps rather than implementation slips: the actionlog hash chain overpromises on truncation detection, actionlog lacks the tenant-scoped point-read wrapper its sibling approval package added for the identical IDOR pattern, and a few consistency gaps (control-char policy, cursor signer hygiene) exist between deliberately mirrored components.

> This scope is high-quality, security-conscious code: invariants are enforced at package boundaries with typed sentinels, pagination is signed and capped, tenant scoping is explicit and opt-out-proof, and concurrency-sensitive paths (ComputeCache close/drain, MemoryCache SetNX) show careful, well-tested reasoning with unusually thorough godoc. The defects found are mostly second-order: documentation that has drifted from refactored code (actionlog's canonicalisation contract, approval's Store.Decide), near-verbatim duplication of the cursor signer across sibling packages with diverging capabilities, and a couple of misuse traps in the stateless off-band helpers (SignEntry's missing microsecond truncation being the sharpest). No data-loss or exploitable flaws were identified in the reviewed interface packages.

> This scope is unusually high quality: interfaces are precisely documented (including known limitations like the TenantStore TOCTOU window), invariants carry audit-tag rationale (FR-048..056, L045..L058), inputs are validated at package boundaries, and shutdown/leak paths (weak-pointer sweepers, runtime.AddCleanup watchdogs, WaitGroup-under-mutex Close protocols) show real concurrency care. The surviving defects are correspondingly narrow: a classification race in ComputeCache's singleflight leader bookkeeping that can cancel a shared compute, a slice-aliasing flaw in the actionlog deep-clone, and an admission-policy blind spot in MemoryCache.SetNX — edge cases rather than systemic problems, plus a handful of validation/error-shape polish items.

> This scope is unusually strong for security: HMAC-signed hash-chained audit entries with length-prefixed canonicalisation, constant-time cursor verification, explicit AllTenants opt-ins, secret-length floors, bounded metadata/payload/key inputs, and defensive copies throughout — with in-code rationale citing prior audit findings (FR-048..FR-056, L045..L058). The surviving issues are mostly consistency gaps between deliberately mirrored packages (actionlog lacking approval's TenantStore and cursor-key zeroing, divergent control-character policy on reason fields) plus one acknowledged-but-real non-atomic tenant check in approval's TenantStore mutations. Nothing rose to a directly exploitable CRITICAL/HIGH flaw within the interface packages themselves.

> This scope is unusually high quality: exhaustively documented invariants, sentinel-error discipline, defensive validation at every package boundary, tamper-evident hash-chain design in actionlog, and deliberate handling of known footguns (TOCTOU in approval.TenantStore, singleflight deadline amplification, ristretto write buffering) with audit-ID annotations. The interface packages (actionlog, approval, budget, tenant) are essentially clean; all substantive findings cluster in data/cache, where the interplay between MemoryCache/ristretto teardown and ComputeCache's advisory inflight/claims bookkeeping leaves a few narrow but real concurrency windows (shutdown-race panic, shared-compute cancellation by a misclassified leader, claim-without-value divergence).

> This scope is unusually high quality: interfaces are misuse-resistant by construction (mandatory tenant scoping with explicit AllTenants opt-in, signed keyset cursors, validated field caps mirroring the Postgres schema, panicking option constructors), invariants are enforced at package boundaries shared by all store implementations, and concurrency-sensitive code (ComputeCache close/drain, budget sweeper, SetNX claims) shows careful, well-commented reasoning backed by targeted regression tests. The defects that remain are almost entirely documentation drift (stale canonicalisation contract, references to a removed Store.Decide, an 'Open' constructor that is actually New) and small cross-package inconsistencies, the most notable being the triplicated CursorSigner that has already diverged (Close exists in approval but not actionlog) and MemoryCache.SetNX's true-but-not-stored edge under admission rejection.

> This scope is unusually high quality for a data-interface layer: signed audit entries with per-tenant hash chains, constant-time HMAC comparisons, signed pagination cursors, explicit tenant-scoping contracts with fail-closed validation, defensive copies of secrets, and bounded input everywhere (key lengths, page limits, metadata size/depth, cursor length). Prior audit findings (FR-048..FR-056, L045..L058) are visibly fixed and documented inline. The remaining issues are consistency gaps between sibling packages — actionlog missing the tenant-scoped Get wrapper, control-character policy, and cursor-key zeroing that approval already has — plus one documentation drift on the signed canonical format.

> This scope is unusually high-quality: invariants are mostly enforced by construction (signed hash chains, signed cursors, tenant-scope validation, closed state sets), godoc is extensive and explains rationale/trade-offs, and tricky paths (chain verification across key rotation, singleflight leader abandonment, weak-pointer sweepers) have targeted regression tests. The issues found are largely edge-case behavioral divergences (MemoryCache SetNX claims vs. ristretto eviction, post-Close error surface), misuse-prone defaults (WithEntryCost sizing), and doc/duplication drift (stale canonicalisation spec, twin CursorSigner copies that have already diverged) rather than core logic defects.

> This is a high-quality, heavily-audited scope: the crypto is careful (length-prefixed canonical form for HMAC signing, per-tenant SHA-256 hash chaining, constant-time cursor and signature comparisons, secret.String key wiping, minimum key-length enforcement), input validation is thorough, and multi-tenant scoping is explicit where present (approval.TenantStore, Query.AllTenants opt-in). The findings are hardening/consistency gaps rather than exploitable primitives: the strongest is that actionlog lacks the tenant-scoped read wrapper its sibling approval package deemed necessary against IDOR, plus a control-character validation asymmetry that weakens the more security-sensitive audit surface. No CRITICAL/HIGH crypto or injection flaws were found.

> This scope is unusually high-quality, defensive, security-conscious Go: the actionlog HMAC hash-chain, tenant-scoping opt-in on List queries, the approval IDOR wrapper, and the ComputeCache singleflight/Close draining are all carefully reasoned and thoroughly documented, with the tricky invariants called out in comments and matching test files. The issues found are mostly consistency and hot-path performance polish rather than correctness defects — the one worth prioritizing is that actionlog's audit Reason field, unlike every sibling validator, permits raw control/ANSI characters. Godoc coverage on tricky exported items is excellent throughout.

> This is high-quality, heavily-audited code: tenant-scoping, HMAC signing/hash-chaining, cursor signing, input validation, and immutability (defensive clones) are all thorough and well-documented with explicit FR references. The non-concurrent interface packages (actionlog, approval, budget, tenant) are essentially clean under a correctness/concurrency lens. The real risk concentrates in data/cache's concurrent machinery (ristretto lifecycle + singleflight), where the Close-vs-in-flight teardown race and the aliasing of singleflight-shared results are the notable gaps.

> This scope is unusually well-hardened and heavily audited: HMAC-SHA256 signing with constant-time compares, length-prefixed canonical forms to prevent field-boundary ambiguity, minimum-key-length enforcement, bounded metadata/cursor/limit inputs, explicit tenant-scoping opt-ins on List queries, and secret.String-wrapped signing keys. The crypto and pagination-signing code is solid with no injection or nonce/randomness issues found. The main gaps are consistency ones: actionlog's Reason/metadata string validators are laxer than approval's (control-char injection into an audit trail), and actionlog lacks the tenant-scoped Get wrapper that approval added to close an IDOR.

> This is high-quality, unusually well-documented interface code: the packages lean hard into misuse-resistance (compile-time tenant scoping via Scope, HMAC-signed opaque cursors, mandatory AllTenants opt-in for cross-tenant listings, nil-guarding options that panic at construction, defensive copies for immutability). Invariants are largely enforced by construction and the sentinel-error contracts are consistent across sibling packages. The findings are edge-case/polish rather than core correctness: an unbounded-growth gap for zero-TTL SetNX claims, a cross-package Close() inconsistency, an unguarded zero-value Scope, and redundant canonicalization on the audit-log write path.

> This scope is high-quality, heavily-audited defensive code: extensive validation, immutable clone-on-read/write patterns, careful singleflight + stale-while-revalidate design with well-reasoned rationale comments and cited audit findings. The strongest concurrency risk is in the in-memory cache lifecycle — mutating operations poke the ristretto store with no synchronization against Close, so a concurrent teardown can panic despite the documented concurrent-safety guarantee. The ComputeCache is otherwise carefully guarded, with the main gap being the test-facing Wait() helper bypassing the bgMu Add/Wait ordering the rest of the type relies on.

## Findings

### [MEDIUM] TenantStore mutations use check-then-act; state transition commits before the tenant check can fail

- **Where**: `data/approval/tenantstore.go` (`decideTenant` / `MarkExecuted` fallback)
- **Dimension**: correctness
- **Detail**: Backends implementing `ApproveForTenant` / `RejectForTenant` / `MarkExecutedForTenant` are atomic (preferred path). Generic/in-memory backends still fall back to Get-then-mutate: the state transition can commit before a concurrent reassignment is observed, with only a post-hoc tenant mismatch check. Residual risk remains for non-atomic stores; closing it without a shared atomic API is a larger contract change (v3 / required ForTenant on Store).
- **Suggestion**: Require tenant-scoped mutators on Store, or document that TenantStore is only race-safe against backends that implement the ForTenant methods.

### [LOW] Append canonicalises Metadata up to three times per call, twice while holding the per-tenant append lock

- **Where**: `data/actionlog/actionlog.go:621`
- **Dimension**: performance
- **Detail**: Append runs validMetadata (line 621, which internally calls canonicalJSON to check MaxMetadataBytes), then inside the store's AppendChained build callback runs validate() (line 677) which calls validMetadata/canonicalJSON again, then computeSignature -> canonicalForm -> canonicalJSON a third time (line 680). The second and third canonicalisations (up to 8 KiB JSON marshalling each, plus the reflection-based metadata walk) execute while the store holds the per-tenant lock — for Postgres a SELECT FOR UPDATE row lock — lengthening the serialization window for every concurrent Append on the same tenant. Failure scenario: a tenant with high append concurrency and near-cap metadata sees append latency and lock wait inflate roughly 2x for pure re-computation of already-validated bytes.
- **Suggestion**: Drop the redundant validMetadata pre-check (validate() inside the callback already covers it), or canonicalise metadata once before AppendChained and reuse the bytes in both validation and canonicalForm.

### [LOW] Read path deep-clones every entry twice (store clone + logger clone) via reflection

- **Where**: `data/actionlog/actionlog.go:732`
- **Dimension**: performance
- **Detail**: signedLogger.List calls cloneEntry(e) (line 732) on every row, and signedLogger.Get does the same (line 702) — but the bundled memory store has already deep-cloned each returned entry (memory/memory.go List clones survivors; Get clones too). Entry.Clone walks Metadata with a fully reflective deep copy (clone.go cloneValue), so a page of up to MaxPageLimit=10,000 entries pays two reflection-based deep copies of every metadata map, on top of the canonicalForm allocation VerifyEntry already performs per row. The defensive clone at the Logger layer is justified for unknown Store implementations, but doubling it for the kit's own stores is pure overhead on the hottest read path.
- **Suggestion**: Document that Store.List/Get must return caller-owned (detached) entries and drop the store-side or logger-side clone, or gate the logger clone behind an interface assertion (e.g. a `CloningStore` marker) so bundled stores aren't cloned twice.

### [LOW] sortedAny has no cycle/depth guard: cyclic Metadata passed to exported SignEntry/VerifyEntry causes unrecoverable stack overflow

- **Where**: `data/actionlog/canonical.go:140`
- **Dimension**: bug
- **Detail**: Logger.Append guards Metadata via validMetadata (which carries a `seen` map and depth cap), but the exported, documented-for-off-band-tools SignEntry and VerifyEntry (actionlog.go lines 798, 832) call computeSignature -> canonicalForm -> canonicalJSON -> sortedAny with no validation. sortedAny (canonical.go line 126) recurses through maps/slices with no visited-set or depth limit, so a self-referential map (m["self"] = m) or deeply nested metadata recurses until goroutine stack exhaustion — a fatal, unrecoverable runtime error that kills the whole process. encoding/json's own cycle detection never gets a chance to return an error because sortedAny overflows first.
- **Suggestion**: Add a visited-pointer set (mirroring cloneValue/walkMetadata) or a depth cap to sortedAny and return an error, or run validMetadata inside SignEntry/VerifyEntry before canonicalising.

### [LOW] Cursor HMAC lacks domain separation and query binding — cursors interchangeable across surfaces and tenants

- **Where**: `data/actionlog/cursor.go:77`
- **Dimension**: security
- **Detail**: The signed payload is exactly `RFC3339Nano + "|" + id` with no package/context prefix, and the docs (lines 44-48) state the wire format deliberately mirrors the approval and auditlog cursor signers (approval/cursor.go line 93 builds a byte-identical payload). If a deployment reuses one signing key across these surfaces (plausible — the doc says the key is 'typically rotated together with other admin-API secrets'), a cursor minted by one signer verifies under another. The cursor is also not bound to the query that produced it (tenant, actor, action, time filters), so a cursor obtained from one listing context is valid for any other query on the same key. Practical impact is limited because List still applies tenant filters server-side, but it weakens the documented 'callers cannot forge cursors to skip ahead' property to 'callers cannot mint positions they never legitimately received from ANY surface sharing the key'.
- **Suggestion**: Prefix the MAC input with a per-package context string (e.g. "actionlog-cursor:v1\x00"), and consider folding Query.TenantID into the signed payload so cursors are bound to the scope they were issued for.

### [LOW] Metadata size cap enforced only after full canonical JSON marshal; individual string values have no length limit

- **Where**: `data/actionlog/metadata.go:50`
- **Dimension**: security
- **Detail**: validMetadata walks the structure first (bounding node count, entry count, array length, depth) but validMetadataString (line 147-149) imposes no length cap on individual string values. A metadata map with one multi-hundred-MB string passes walkMetadata (1 node, 1 entry), and only then does canonicalJSON marshal the entire value — allocating a buffer at least the size of the input — before the len(raw) <= MaxMetadataBytes (8 KiB) check at line 51 rejects it. In Logger.Append this runs before any other validation (actionlog.go line 621), so a caller-influenced metadata bag causes a large transient allocation for input that was always going to be rejected. Attack surface is limited to Append callers (in-process), but a cheap pre-check would remove the amplification entirely.
- **Suggestion**: Cap string value length in validMetadataString (e.g. MaxMetadataBytes), or accumulate an approximate byte total during walkMetadata and reject before calling canonicalJSON.

### [LOW] ValidateForCreate never validates CreatedAt, allowing zero or far-future creation timestamps that stores paginate and expire against

- **Where**: `data/approval/approval.go:281`
- **Dimension**: bug
- **Detail**: ValidateForCreate (line 281) checks ExpiresAt (must be set and future) and rejects pre-set DecidedBy/DecidedAt, but places no constraint on r.CreatedAt. Store.List orders and keyset-paginates on (CreatedAt, ID) and Decide's expiry logic is documented as "CreatedAt + ttl has passed". A direct store caller that leaves CreatedAt zero (or sets it in the future) creates a request that sorts to the wrong end of every listing and, for TTL-based expiry paths, is treated as expired immediately (zero) or never (future) — inconsistent with the bounded-decision-window invariant the same function enforces via ExpiresAt.
- **Suggestion**: Require a non-zero CreatedAt and reject CreatedAt after now (with small skew tolerance) in ValidateForCreate, mirroring the ExpiresAt checks.

### [LOW] Request.Payload is only size-capped, never validated as JSON

- **Where**: `data/approval/approval.go:292`
- **Dimension**: error-handling
- **Detail**: ValidateForCreate checks len(r.Payload) <= MaxPayloadSize but never verifies the json.RawMessage holds syntactically valid JSON. Failure scenario: a direct Store caller persists a payload of arbitrary non-JSON bytes; the failure then surfaces late and inconsistently per backend — a Postgres jsonb column rejects the INSERT with a low-value database error (the exact late-failure pattern FR-055/FR-056 were fixed to avoid for ID and size), the memory store accepts it, and any later json.Marshal of the Request (e.g. rendering the pending approval to an operator UI) errors at encode time because encoding/json refuses invalid RawMessage bytes, potentially making the approval undecidable through JSON-based tooling.
- **Suggestion**: Add `if len(r.Payload) > 0 && !json.Valid(r.Payload) { return ErrInvalidRequest }` to ValidateForCreate so both stores share the contract.

### [LOW] Unexported validate() wrapper is dead code kept alive only by its test

- **Where**: `data/approval/approval.go:333`
- **Dimension**: smell
- **Detail**: func validate(r Request, now time.Time) error { return ValidateForCreate(r, now) } has no production caller anywhere in the package (grep confirms the only reference is approval_test.go:69). It is a zero-value indirection that exists solely so a test can call it, which also means the test exercises the wrapper instead of the exported API. Failure scenario: none at runtime; it misleads readers into thinking there is an internal validation path distinct from ValidateForCreate.
- **Suggestion**: Delete validate() and point the test at ValidateForCreate directly.

### [LOW] Oversized staleTTL is validated per-compute instead of at construction

- **Where**: `data/cache/compute.go:528`
- **Dimension**: api-design
- **Detail**: WithStaleTTL only panics on negative values (compute.go line 66), while the cfg.staleTTL > maxCacheTTL (~10y) check lives inside executeCompute (line 528) and therefore fires on every compute at runtime. The invariant is a pure construction-time property — nothing about it depends on the computed value. Failure scenario: a service configured with WithStaleTTL(20 * 365 * 24 * time.Hour) constructs successfully, passes startup checks, and then every single GetOrCompute miss fails with 'staleTTL exceeds maximum', turning a config error into a production-wide cache outage instead of a startup failure.
- **Suggestion**: Reject staleTTL > maxCacheTTL inside WithStaleTTL (or NewComputeCache), matching the package's fail-at-construction convention, and keep only the ttl (return value) check in executeCompute.

### [LOW] MemoryCache.SetNX can return true when the value was never actually stored

- **Where**: `data/cache/memory_cache.go:501`
- **Dimension**: api-design
- **Detail**: SetNX records an nxClaim and returns true after mc.Set + cache.Wait(), but Ristretto's TinyLFU admission policy can still drop the enqueued write when the buffer is processed (the struct's own comment acknowledges 'its TinyLFU admission policy may silently reject entries, so a subsequent Get cannot reliably observe a prior SetNX'). The BulkCache contract in cache.go states SetNX 'Returns true when the value was stored' — so under memory pressure a caller can get (true, nil) from SetNX and ErrCacheMiss from the very next Get. Patterns that write an owner token via SetNX and read it back to confirm ownership silently break on this backend while working on Redis.
- **Suggestion**: Either verify the value landed after cache.Wait() (Get and treat absence as ErrAdmissionRejected without recording a claim), or document on BulkCache.SetNX/MemoryCache.SetNX that true means 'claim acquired' and the value may still not be readable from this backend.

### [LOW] All SetNX and Delete operations serialize on one global mutex with ristretto buffer flushes inside the critical section

- **Where**: `data/cache/memory_cache.go:508`
- **Dimension**: performance
- **Detail**: MemoryCache.SetNX takes the single setNXMu and calls mc.cache.Wait() up to twice while holding it (lines 524, 535); Delete takes the same mutex and also calls Wait() (lines 561–568). cache.Wait() drains ristretto's entire write buffer, so operations on completely unrelated keys queue behind each other and behind full buffer flushes. Failure scenario: a service using MemoryCache as an L1 for per-request idempotency claims (many concurrent SetNX on distinct keys) sees throughput collapse to sequential buffer-flush latency under load, despite ristretto being chosen for concurrency.
- **Suggestion**: Shard the mutex by key (e.g. a fixed array of mutexes indexed by key hash) or keep per-key claim state so unrelated SetNX/Delete calls do not contend on one lock and one buffer flush.

### [LOW] SetNX claim can outlive its evicted cache entry, blocking the key until TTL — permanently for ttl=0

- **Where**: `data/cache/memory_cache.go:511`
- **Dimension**: bug
- **Detail**: SetNX records an nxClaim after a successful Set, and later SetNX calls return false while the claim is unexpired (lines 511-517) regardless of whether the value is still in ristretto. Ristretto's cost-based eviction can drop the entry at any time; the claim map is only cleared by Delete, claim-TTL expiry, or the 60s sweeper. After eviction, the key is in a state where Get/Exists report absent but SetNX keeps returning false — for ttl=0 claims (zero expiresAt, line 513/537-540) this lasts until an explicit Delete, i.e. forever in practice. A compute-once/lock user whose value is evicted under memory pressure sees the slot both empty and unclaimable, a denial of the very compute-once path SetNX exists for.
- **Suggestion**: When a claim exists but mc.cache.Get(key) misses (entry evicted/expired), drop the claim and allow the new claim to proceed; or register a ristretto OnEvict handler that deletes the corresponding nxClaim.

### [LOW] SetNX serializes all NX/Delete ops on one mutex and flushes the entire ristretto write buffer twice per call

- **Where**: `data/cache/memory_cache.go:524`
- **Dimension**: performance
- **Detail**: SetNX holds the process-wide setNXMu (line 508) and calls mc.cache.Wait() at line 524 (before the existence check) and again at line 535 (after Set); Delete also calls Wait() under the same mutex (line 568). Each Wait() drains ristretto's full write buffer. For a cache used as the documented cross-process compute-once / idempotency-key primitive under load, every SetNX both serializes on the single mutex and forces two full buffer flushes, defeating ristretto's batching and creating a throughput bottleneck. This is the inherent cost of correct in-process test-and-set on a buffered store, but it is a real lock-contention/perf characteristic worth flagging.
- **Suggestion**: Consider a per-key or sharded lock instead of one global setNXMu, and see whether a single Wait() (or a claims-only check that avoids the buffer flush) can preserve the NX contract.

### [LOW] Delete serialises on the cache-wide setNXMu and flushes the entire ristretto write buffer on every call

- **Where**: `data/cache/memory_cache.go:568`
- **Dimension**: performance
- **Detail**: Delete (lines 551-571) acquires the single setNXMu shared with all SetNX calls for all keys, then calls cache.Wait(), which blocks until every buffered write across the whole cache is applied. On a busy L1 cache, unrelated Delete/SetNX calls on different keys queue behind one another and each pays a full write-buffer drain. Failure scenario: a workload mixing frequent Set with per-request Delete (e.g. invalidation-on-write) sees Deletes collapse to one-at-a-time throughput with latency proportional to global write volume, defeating ristretto's contention-free design. The strict-visibility rationale is documented, but the global-lock + global-flush cost is not surfaced to callers choosing this as an L1 cache.
- **Suggestion**: Shard the NX mutex by key (or use per-key claims as the lock), and document the Wait() cost on Delete/SetNX; consider making strict Delete visibility opt-in.

### [LOW] TypedCache hardcodes encoding/json and ignores the package's own Codec abstraction

- **Where**: `data/cache/typed_cache.go:14`
- **Dimension**: api-design
- **Detail**: codec.go defines Codec[T] precisely so serialization is pluggable, and ComputeCache offers NewComputeCacheWithCodec; TypedCache, the other typed wrapper in the same package, calls json.Marshal/json.Unmarshal directly (lines 71, 83) with no codec variant. A caller who needs a non-JSON encoding (or a type with custom binary marshalling) can use it with ComputeCache but not with TypedCache, and the two sibling wrappers also duplicate the identical fullKey prefix/length logic (typed_cache.go:45 vs compute.go:229).
- **Suggestion**: Give TypedCache a codec field defaulting to JSONCodec[T] plus a NewTypedCacheWithCodec constructor mirroring ComputeCache, and share the fullKey helper.

### [LOW] Cache prefixes are concatenated with no enforced delimiter — silent keyspace collision between wrappers

- **Where**: `data/cache/typed_cache.go:24`
- **Dimension**: api-design
- **Detail**: NewTypedCache and NewComputeCache (data/cache/compute.go:175) prepend the prefix verbatim (full = prefix + key) and only document that callers 'MUST include a trailing separator'. Nothing validates it, so the safety of the shared-backend keyspace rests entirely on caller discipline. Failure scenario (from the code's own comment): prefix "user" with key "s1" and prefix "users" with key "1" map to the same backend key, letting two caches silently read/overwrite each other's envelopes; if prefixes embed identifiers (e.g. "tenant"+id built by hand instead of via tenant.Scope.Key), tenant "1" + key "2x" collides with tenant "12" + key "x", turning a naming slip into cross-tenant cache data exposure.
- **Suggestion**: Have ValidateKeyPrefix (or the constructors) reject non-empty prefixes that do not end in a separator byte that ValidateKey forbids in keys is not possible (keys allow ':'), so instead length-prefix or hard-join with a reserved delimiter internally, or at minimum require the prefix to end with ':' and document keys must be built via tenant.Scope.Key for tenant-scoped use.

### [LOW] TypedCache hardcodes encoding/json instead of using the package's own Codec[T] abstraction

- **Where**: `data/cache/typed_cache.go:33`
- **Dimension**: api-design
- **Detail**: The package defines Codec[T] (codec.go) and ComputeCache supports pluggable codecs via NewComputeCacheWithCodec, but TypedCache calls json.Marshal/json.Unmarshal directly (lines 71, 83). Sibling inconsistency within the same package: a caller who stores protobuf or msgpack values via a custom codec in ComputeCache cannot get the same treatment from TypedCache and has to drop to raw Cache. Failure scenario: a team standardises on a msgpack Codec for ComputeCache, then adds a TypedCache over the same backend and silently gets JSON-encoded values that the rest of their tooling cannot decode.
- **Suggestion**: Add a codec Codec[T] field defaulting to JSONCodec[T] plus a NewTypedCacheWithCodec constructor, mirroring ComputeCache.

