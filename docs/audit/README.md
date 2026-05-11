# rho-kit Audit & Roadmap

This directory holds the kit's enduring audit and security documents.
Per-package implementation plans for the v1→v2 audit waves and the
v2.0.0 new-package proposals lived here historically (`existing/00-17`,
`new/01-29`); they were scratchpads guiding implementation and have
been removed now that the implementation has shipped. Per-finding
status is preserved in [CRITICAL.md](CRITICAL.md) and in `git log`;
deferred follow-ups carry forward in [ROADMAP.md](ROADMAP.md).

Release-candidate artifacts live in [../release](../release/). Use those files
for API-freeze, migration, and final tag evidence; use this directory for the
security/audit history that explains why the release surface exists.

## Documents

| File | Purpose |
|---|---|
| [THREAT_MODEL.md](THREAT_MODEL.md) | STRIDE-style threat surface; assets, adversaries, mitigations, shipped gap closures, and remaining follow-up list. Updated whenever a new threat ID lands. |
| [SUPPLY_CHAIN.md](SUPPLY_CHAIN.md) | Pinning policy, direct dependency source allowlist, heavy SDK boundary guard, dependabot cadence, build flags, CycloneDX SBOM, vulnerability response SLO, license allowlist. |
| [dependency-allowlist.txt](dependency-allowlist.txt) | Exact review ledger for direct external Go module dependencies; enforced by `make check-dependency-allowlist`. |
| [ROADMAP.md](ROADMAP.md) | What shipped per phase + what's deferred to v2.1+ (cloud KMS, k8slease/etcd, Kafka, dashboards subset, kit-new flags, per-package benchmarks). |
| [CRITICAL.md](CRITICAL.md) | Historical per-finding ledger of the 12 CRITICAL items + closely-related HIGH cluster from the v1→v2 audit. All closed; kept for the audit trail. |

## How findings flow now

1. New threats land in [THREAT_MODEL.md](THREAT_MODEL.md) under the
   relevant §4 sub-section (or a new sub-section if none fits) plus
   the §8 gap list if no in-kit mitigation exists yet.
2. Implementation work is tracked in conventional-commit messages
   and (for multi-step work) in PR descriptions — not in this
   directory.
3. Closed findings are referenced by commit hash inline in the
   threat-model entry and, for items that originated as a CRITICAL,
   in [CRITICAL.md](CRITICAL.md).
4. The vulnerability-response SLO in [SUPPLY_CHAIN.md](SUPPLY_CHAIN.md)
   §"Vulnerability response" governs HIGH/CRITICAL response time.

## Recurring patterns the audit closed

The original audit identified four recurring patterns; all are now
closed:

1. **Hardening off by default** — recover middleware, audience
   validation, mandatory publish, parent-dir fsync, owner-token unlock,
   Postgres `sslmode=require`, RetryIfNotPermanent, transaction-required
   outbox writes, CSRF shared secret, idempotency TTL > 0.
2. **Interface drift** — `data/lock` interface and Redis impl,
   `data/idempotency.Store` request-fingerprint, `outbox.Writer`
   transaction enforcement, idempotency TTL semantics across backends.
3. **Constructors accept nil dependencies** — fail at first use
   instead of startup. Now: nil-deps panic at construction.
4. **Observability loud-by-default** — 100% trace sample rate,
   Baggage propagator on, Prometheus default histogram buckets that
   topped out at 10s, default trusted-proxies trusting all RFC1918.

The Builder runs `validateProductionSafety()` unconditionally inside
`Build()`; per-relaxation `Without*()` opt-outs are the only supported
relaxation, and there is no `KIT_ENV` escape hatch in any kit code
path.
