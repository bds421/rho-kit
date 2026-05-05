# rho-kit Audit & Roadmap (2026-05)

Five parallel audits across all 50+ Go modules, plus integration of a separate independent audit (`docs/ai/repo-audit-2026-05-05.md`) that ran the build/test tooling. ~125 findings total. The kit's primitives are correct; its **defaults, middleware-stack composition, and dep freshness** ship insecure. Four recurring patterns:

1. **Hardening off by default** — recover middleware, audience validation, mandatory publish, parent-dir fsync, owner-token unlock, Postgres `sslmode=require`, RetryIfNotPermanent, transaction-required outbox writes, CSRF shared secret, idempotency TTL > 0.
2. **Interface drift** — `data/lock` interface and Redis impl don't match; `data/idempotency.Store` lacks the request-fingerprint the middleware computes; `outbox.Writer` doesn't enforce ambient transaction; idempotency TTL semantics differ between Redis/Memory/PG backends.
3. **Constructors accept nil dependencies** — fail at first use (request time), not at startup. Violates the kit's own AGENTS.md fail-fast convention.
4. **Observability loud-by-default** — 100% trace sample rate, Baggage propagator on, Prometheus default histogram buckets that top out at 10s, default trusted-proxies trusting all RFC1918.

## Severity counts

| | CRITICAL | HIGH | MEDIUM | LOW |
|---|---|---|---|---|
| Findings | 12 | ~58 | ~38 | ~17 |

CRITICAL = security loss or data loss in normal operation. HIGH = correctness bug in common path. MEDIUM = correctness bug in rare path or significant footgun. LOW = nits worth listing.

See [CRITICAL.md](CRITICAL.md) for the cross-package critical-fix list. See [ROADMAP.md](ROADMAP.md) for suggested execution order (6–10 weeks of focused work to close CRITICAL+HIGH).

## How this directory is organized

- `existing/` — one file per affected package (or natural group). Lists findings, fixes, and migration notes for code that already exists. Read these in priority order from ROADMAP.
- `new/` — one file per proposed new package. Each spec includes purpose, public API sketch, and how it integrates with the Builder / golden path.

## Conventions used in finding files

Each finding looks like:

```
### [SEVERITY] Short title
**File**: path/file.go:LINE
**Issue**: What's wrong + which code path triggers it.
**Fix**: Recommended change.
**Effort**: S (≤1 day) / M (1–3 days) / L (>3 days)
**Migration**: only if the fix breaks a published API.
```

## Index

### Existing packages — fix plans

| Area | File | Critical | High |
|---|---|---|---|
| **Cross-cutting (deps, tooling, conventions)** | [existing/00-cross-cutting.md](existing/00-cross-cutting.md) | **1** | 1 |
| Builder + app wiring | [existing/01-app-and-builder.md](existing/01-app-and-builder.md) | – | 2 |
| core/* | [existing/02-core.md](existing/02-core.md) | – | 1 |
| crypto + security | [existing/03-crypto-and-security.md](existing/03-crypto-and-security.md) | – | 7 |
| httpx server + client | [existing/04-httpx-server-and-client.md](existing/04-httpx-server-and-client.md) | – | 5 |
| httpx middleware | [existing/05-httpx-middleware.md](existing/05-httpx-middleware.md) | 1 | 11 |
| grpcx | [existing/06-grpcx.md](existing/06-grpcx.md) | 1 | – |
| data/lock + data/queue | [existing/07-data-lock-and-queue.md](existing/07-data-lock-and-queue.md) | 3 | 4 |
| data/cache + data/idempotency | [existing/08-data-cache-and-idempotency.md](existing/08-data-cache-and-idempotency.md) | 1 | 7 |
| data/stream | [existing/09-data-stream.md](existing/09-data-stream.md) | – | 1 |
| infra/messaging (AMQP + buffered) | [existing/10-infra-messaging.md](existing/10-infra-messaging.md) | 2 | 3 |
| infra/outbox | [existing/11-infra-outbox.md](existing/11-infra-outbox.md) | 1 | 2 |
| infra/storage | [existing/12-infra-storage.md](existing/12-infra-storage.md) | 1 | 4 |
| infra/sqldb + infra/redis | [existing/13-infra-sqldb-redis.md](existing/13-infra-sqldb-redis.md) | 1 | 2 |
| runtime/* | [existing/14-runtime.md](existing/14-runtime.md) | – | 8 |
| resilience/* | [existing/15-resilience.md](existing/15-resilience.md) | – | 1 |
| observability/* | [existing/16-observability.md](existing/16-observability.md) | – | 4 |
| io/* | [existing/17-io.md](existing/17-io.md) | – | 2 |

### New packages — proposals

| Tier | File | Why |
|---|---|---|
| 1 | [new/01-httpx-middleware-recover.md](new/01-httpx-middleware-recover.md) | Closes CRITICAL: panic recovery for HTTP handlers |
| 1 | [new/02-grpcx-recovery-default.md](new/02-grpcx-recovery-default.md) | Closes CRITICAL: panic recovery for gRPC |
| 1 | [new/03-crypto-passhash.md](new/03-crypto-passhash.md) | Argon2id helper (most-asked missing primitive) |
| 1 | [new/04-crypto-envelope.md](new/04-crypto-envelope.md) | Envelope encryption + KMS providers (rotation) |
| 1 | [new/05-crypto-paseto.md](new/05-crypto-paseto.md) | Safer alternative to JWT for new services |
| 1 | [new/06-security-csrf-tokens.md](new/06-security-csrf-tokens.md) | Session-bound CSRF primitive (current middleware is incomplete) |
| 1 | [new/07-security-secret-string.md](new/07-security-secret-string.md) | `SecretString` type that refuses to print/marshal |
| 1 | [new/08-security-csp-nonce.md](new/08-security-csp-nonce.md) | Per-request CSP nonce middleware |
| 2 | [new/09-data-lock-pg-advisory.md](new/09-data-lock-pg-advisory.md) | Postgres advisory lock (recommended in redislock docs) |
| 2 | [new/10-data-ratelimit-sliding-window.md](new/10-data-ratelimit-sliding-window.md) | GCRA / token bucket (current limiter is fixed-window) |
| 2 | [new/11-infra-leader-election.md](new/11-infra-leader-election.md) | k8s-lease / etcd / pg-advisory leader election |
| 2 | [new/12-infra-messaging-nats.md](new/12-infra-messaging-nats.md) | NATS JetStream backend |
| 2 | [new/13-infra-messaging-kafka.md](new/13-infra-messaging-kafka.md) | Kafka backend |
| 2 | [new/14-infra-sqldb-pgx.md](new/14-infra-sqldb-pgx.md) | `pgx`-native option (LISTEN/NOTIFY, COPY, batched pipelines) |
| 3 | [new/15-observability-pprof-runtime.md](new/15-observability-pprof-runtime.md) | `/debug/pprof` + runtime metrics on internal port |
| 3 | [new/16-observability-red-metrics.md](new/16-observability-red-metrics.md) | RED middleware with sane buckets |
| 3 | [new/17-httpx-problem-details.md](new/17-httpx-problem-details.md) | RFC 7807 writer alongside `WriteError` |
| 6 | [new/18-tools-kit-doctor.md](new/18-tools-kit-doctor.md) | CLI that scans a service's wiring for dangerous defaults |
| 2 | [new/19-app-production-defaults.md](new/19-app-production-defaults.md) | **One switch that flips every phase-1 hardening** |
| 5 | [new/20-multitenant-primitives.md](new/20-multitenant-primitives.md) | Tenant context, tenant-safe Redis keys, label allowlists |
| 6 | [new/21-tools-kit-new.md](new/21-tools-kit-new.md) | Scaffold generator (companion to kit-doctor) |
| 3 | [new/22-observability-dashboards.md](new/22-observability-dashboards.md) | Grafana dashboards + Prometheus alert templates |
| 6 | [new/23-tools-bench-gate.md](new/23-tools-bench-gate.md) | CI benchmark regression gate |
| 5 | [new/24-httpx-signed-requests.md](new/24-httpx-signed-requests.md) | Signed-request middleware + client RoundTripper with replay cache |
| 5 | [new/25-storagehttp-upload-security.md](new/25-storagehttp-upload-security.md) | MIME sniffing, AV adapter, dimension limits, quotas |
