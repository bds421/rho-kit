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
| LOW | 1 |
| **Total (deduplicated)** | **2** |

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

### [LOW] Read path deep-clones every entry twice (store clone + logger clone) via reflection

- **Where**: `data/actionlog/actionlog.go:732`
- **Dimension**: performance
- **Detail**: signedLogger.List calls cloneEntry(e) on every row, and signedLogger.Get does the same — but the bundled memory store has already deep-cloned each returned entry. The defensive clone at the Logger layer is justified for unknown Store implementations, but doubling it for the kit's own stores is pure overhead on the hottest read path.
- **Suggestion**: Gate the logger clone behind an interface assertion (e.g. a `CloningStore` marker) so bundled stores aren't cloned twice.

