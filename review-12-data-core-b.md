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
| LOW | 2 |
| **Total (deduplicated)** | **3** |

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


### [LOW] Consumer.Consume returns no error, forcing implementations to swallow fatal backend failures

- **Where**: `data/queue/queue.go:136`
- **Dimension**: error-handling
- **Detail**: Both queue.Consumer.Consume (data/queue/queue.go:136) and stream.Consumer.Consume (data/stream/stream.go:30) are declared as blocking methods with no return value: "blocks and processes messages until ctx is cancelled". An implementation whose backend connection dies permanently (auth revoked, stream deleted, network partition beyond retry budget) has no channel to report that terminal failure — it can only log and return, and the caller cannot distinguish a clean ctx-cancel exit from a consumer that silently died, so a service keeps running while consuming nothing. Every other blocking loop in this scope (e.g. idempotency MemoryStore.Run) returns error precisely so lifecycle runners can detect abnormal exits.
- **Suggestion**: Change the interface to Consume(ctx, queue, handler) error (returning nil on ctx cancellation, non-nil on terminal backend failure), matching the MemoryStore.Run convention and typical lifecycle-runner wiring.

### [LOW] stream.Consumer.Consume also lacks an error return (same defect as queue.Consumer)

- **Where**: `data/stream/stream.go:30`
- **Dimension**: api-design
- **Detail**: Same issue as data/queue/queue.go:136: Consume blocks until ctx is cancelled and returns nothing, so a redisstream consumer that permanently loses its connection or whose consumer group is deleted has no channel to report a fatal error; the caller cannot distinguish clean shutdown from silent failure.
- **Suggestion**: Add an error return in lockstep with the queue.Consumer change.

