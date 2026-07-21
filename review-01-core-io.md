# Code review: Core primitives & IO (stage 1 — unverified findings)

## Scope

- **Directories**: core/, io/
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 3 (lenses inferred: correctness, design, security; expected lens count: 3)
- Status: raw reviewer findings; adversarial verification (stage 2) pending.

## Summary

| Severity | Count |
|---|---|
| CRITICAL | 0 |
| HIGH | 0 |
| MEDIUM | 0 |
| LOW | 0 |
| **Total (deduplicated)** | **0** |

**Reviewer impressions:**

> This scope is unusually high quality for its size: constant-time secret comparison with length-equalization, rejection-sampled crypto/rand token generation, length-prefixed tenant keys that are provably collision-free, a TLS-clone helper that refuses InsecureSkipVerify by default, redaction-first error wrapping, and size caps on every file/env input — with comments citing prior hostile-review waves that closed earlier findings. The remaining issues are second-order: check-then-use races in atomicfile's symlink/size defenses, fail-open behavior for typo'd validation constraints/formats, and one inherited-TLS-setting gap (KeyLogWriter) that the package's own normalization contract arguably should cover. Nothing found rises to an exploitable CRITICAL in typical deployments.

> This scope is unusually high quality for infrastructure code: small, cohesive packages with exhaustive godoc that documents not just behavior but rationale and rejected alternatives, systematic hardening from prior adversarial reviews (length caps, redaction, symlink refusal, constant-time comparison, freeze-before-use invariants), and consistent conventions across siblings. The surviving findings are mostly polish and contract drift — the two most substantive are the id.Generator test seam that is wired to nothing (its documented purpose cannot work) and the validate package's fail-open handling of typo'd constraint tag values, which silently weakens the very validation the package exists to enforce.

> This scope is exceptionally polished for its size: pervasive input caps, redaction discipline, correct sync primitives (Watchable's setMu/mu split, secret.String's pointer-inner design, CAS-based clock stub), and inline records of prior hostile-review fixes. The concurrency-sensitive code (config watchers, validate's freeze-and-snapshot format registry, atomic time) is essentially sound; the remaining defects are lifecycle edge cases (unretryable Start, silent watcher death) and narrow TOCTOU gaps in atomicfile that fall inside the package's own stated threat model. Nothing rises to HIGH or CRITICAL.

## Findings

_All stage-1 findings for this family are fixed or applied as intentional v2 breaks. See V3_BREAKING_PROPOSALS.md (APPLIED) and git history._
