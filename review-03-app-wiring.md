# Code review: App DI & wiring (stage 1 — unverified findings)

## Scope

- **Directories**: app/
- **Git ref**: main @ 9c370ea2 (v2.3.1 prep)
- **Review lens results**: 9 (lenses inferred: correctness, design, security; expected lens count: 3)
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

> This is unusually disciplined infrastructure code: the lazy bridge-module pattern is applied consistently across ~24 sub-packages, invariants are aggressively enforced at construction time (panic-on-nil, affirmative security opt-outs, duplicate-provider guards), every submodule has tests, and the godoc is extensive. The findings are correspondingly modest — the most substantive issues are a doc/behavior contradiction on server-option ordering that undermines a stated mTLS guarantee, an AMQP plaintext-exemption leak in the static-URL-plus-provider combination, and a complete test gap on the security-ordering-critical phased middleware composer. The rest is doc drift from the v2 refactor and small cross-package convention inconsistencies (missing ModuleName constants, magic-string lookups).

> This is an unusually security-conscious composition layer: fail-closed validators (TLS, loopback-only internal ops, mandatory rate-limit declaration, JWT issuer/audience pinning), per-transport plaintext opt-ins, consistent secret redaction (LogValue implementations, redact.Error, boolean-only config logging), and InsecureSkipVerify rejection in the TLS clone paths. The defects found are not classic injection or crypto bugs but edge-case interactions between the affirmative-declaration guards — places where an exemption or capability marker leaks beyond the case it was designed for (amqp loopback exemption + URL provider, Keyed satisfying the rate-limit validator, cron's silent loss of leader gating) plus one documentation claim about server-option ordering that is the inverse of actual behavior. Code quality, comments, and fail-fast discipline are otherwise excellent and consistent across all 23 bridge modules.

> This scope is high quality: the Builder's lifecycle is carefully sequenced with explicit happens-before reasoning (the lateBgs freeze guard, captured-pool health-check closures, stopOnce-idempotent gRPC stop, detached cleanup contexts), and the bridge modules are consistently defensive (option cloning, nil panics, TLS transport-safety gates, watchdogged module Init). The defects found live at the seams between modules rather than inside any single one — cross-module ordering (cron/leader), a safety check evaluated against the wrong input (amqp static URL vs. URL provider), and a documented security-ordering contract the Builder inverts. Most shutdown/concurrency trade-offs are explicitly documented as accepted, which makes the remaining silent-degradation paths stand out as the main risk.

> This scope is unusually well-hardened for infrastructure wiring code: fail-closed validators (TLS-required, loopback-only internal ops, mandatory rate-limit and JWT issuer/audience declarations), consistent explicit Without*() opt-outs, TLS version floors with InsecureSkipVerify rejection, and disciplined secret redaction (redact.Error, slog.LogValuer on configs like natsbackend.Config). The findings are mostly seams where one subsystem's opt-out or declaration silently covers (or fails to cover) a different transport — the AMQP loopback exemption leaking into provider-driven dials, gRPC inheriting the HTTP plaintext opt-out without its own acknowledgment, and a documented option-ordering guarantee the Builder does not actually implement. No injection, crypto-misuse, or tenant-isolation defects were found in this layer.

> This scope is unusually high quality: the lazy-adapter bridge pattern is applied consistently across all 23 submodules, misuse-resistance is taken seriously (construction-time panics, always-on production validators with explicit Without*() opt-outs, defensive option cloning), and shutdown/init lifecycles are carefully sequenced with panic recovery and timeouts. The main weaknesses are documentation drift left over from the v2.0.0 refactor (the core Builder docs still describe removed With* methods and an init-order contract RunContext no longer honors), a few cross-module lookup edges that degrade to opaque or silent failures (leader-before-postgres, cron's magic-string leader lookup, the missing NATS health check), and minor convention inconsistencies between otherwise near-identical sibling bridges.

> The app DI/wiring family is unusually high quality for this class of code: lifecycle ordering (init/populate/router/serve/drain) is carefully sequenced, happens-before edges are real (Init runs behind a watchdog channel, late-registration is frozen under a mutex, stops are idempotent via sync.Once or nil-swap), and nearly every sharp edge carries a comment citing the audit finding it fixes. The thin bridge modules (actionlog, approval, authz, auditlog, flags, slo, storage, tenant, etc.) are essentially defect-free glue. The remaining issues are edge-case interactions between modules — silent degradation when cross-module lookups happen before a peer's Init (cron/leader), config exemptions computed against inputs that another option supersedes (amqp URL provider), and one shutdown path (postgres) that ignores its deadline context.

> This scope is unusually high quality: the Builder and every bridge module follow a consistent fail-closed philosophy (affirmative Without*() opt-outs, construction-time panics on misconfiguration, loopback-only internal ops, mTLS-by-default, redacted error/config logging with slog.LogValuer, defensive option/slice cloning). The findings are almost all consistency gaps at seams between subsystems — the NATS bridge missing the transport-safety check its redis/amqp siblings have, the gRPC listener escaping the HTTP-centric rate-limit contract, and one documented security ordering guarantee (app/http.WithServerOption) that the Builder's actual option order does not honor. No injection, crypto misuse, or secret-leak issues were found in this family.

> This is unusually disciplined infrastructure code: the app root and all 24 bridge modules follow a rigorously consistent pattern (fail-fast panics on construction misuse, affirmative security opt-outs, capability interfaces instead of imports, defensive slice/config cloning, redacted logging), and the doc comments explain not just what but why, including deliberate trade-offs. The findings are correspondingly modest — the notable gaps are two silent-degradation edge cases that contradict the kit's fail-loud philosophy (cron leader gating, the AMQP plaintext exemption leaking onto provider URLs) and a missing unit test for the security-ordering-critical phased-middleware composer; the rest is polish. Test coverage elsewhere in the scope is thorough, including regression tests pinned to audit finding IDs.

> This is unusually high-quality infrastructure code: the module lifecycle (init in order, populate, run, reverse-order stop) is carefully sequenced so almost all shared state is written before any concurrency begins, mutable fields captured by long-lived closures are deliberately snapshotted (postgres health check, redis metrics collector), and Stop paths are idempotent (grpc stopOnce) with panic recovery and detached timeout contexts throughout. The suspected heavyweight bugs (pgxpool close deadlock via the stdlib sql.DB, runner-func semantics, freeze-guard races on lateBgs/customReadiness/healthChecks) all check out clean on verification. What remains are edge-case ordering hazards (cron/leader silent degradation), doc-vs-implementation drift on two contracts, and small lifecycle/consistency gaps — no critical concurrency or data-loss defects.

## Findings

_All stage-1 findings for this family are fixed or applied as intentional v2 breaks. See V3_BREAKING_PROPOSALS.md (APPLIED) and git history._
