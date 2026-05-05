# CRITICAL findings (cross-package)

Twelve findings that cause data loss, security loss, or both in normal operation. Fix all of these before shipping any other audit work. Effort estimates assume one engineer; many can land in parallel.

| # | Title | File | Detail in |
|---|---|---|---|
| 1 | **Vulnerable Go runtime + grpc dep** (11 reachable CVEs reported by `make vulncheck`) | `go.work:1` + every `go.mod` | [existing/00](existing/00-cross-cutting.md) |
| 2 | Default middleware stack has no panic recovery | `httpx/middleware/stack/stack.go:41-121` | [existing/05](existing/05-httpx-middleware.md), [new/01](new/01-httpx-middleware-recover.md) |
| 3 | `grpcx.NewServer` does not install Recovery interceptors | `grpcx/server.go:105-141` | [existing/06](existing/06-grpcx.md), [new/02](new/02-grpcx-recovery-default.md) |
| 4 | AMQP publisher silently drops unroutable messages (mandatory=false) | `infra/messaging/amqpbackend/publisher.go:117-119` | [existing/10](existing/10-infra-messaging.md) |
| 5 | `debughttp` Publish/Consume endpoints have no auth | `infra/messaging/amqpbackend/debughttp/debug.go:42-99,108-161` | [existing/10](existing/10-infra-messaging.md) |
| 6 | Outbox tight retry loop with no backoff | `infra/outbox/gormstore/gormstore.go:188-201` | [existing/11](existing/11-infra-outbox.md) |
| 7 | Local storage doesn't fsync parent directory after rename | `infra/storage/localbackend/local.go:80-92` | [existing/12](existing/12-infra-storage.md) |
| 8 | Postgres `sslmode` defaults to `disable` | `infra/sqldb/gormdb/gormpostgres/driver.go:90` | [existing/13](existing/13-infra-sqldb-redis.md) |
| 9 | pgstore idempotency `Unlock` has no owner check (split-brain) | `data/idempotency/pgstore/store.go:165-175` | [existing/08](existing/08-data-cache-and-idempotency.md) |
| 10 | Redis queue uses one shared `:processing` list across consumers | `data/queue/redisqueue/queue.go:399-410` | [existing/07](existing/07-data-lock-and-queue.md) |
| 11 | Redis queue `LRem`-by-data races + recovery silently drops messages | `data/queue/redisqueue/helpers.go:99-219` | [existing/07](existing/07-data-lock-and-queue.md) |
| 12 | `data/lock` interface and redislock implementation are incompatible | `data/lock/lock.go:13-18` | [existing/07](existing/07-data-lock-and-queue.md) |

## Suggested order

Item 1 (dep vulns) gates everything — the runtime upgrade may shake out behavior changes that affect the other fixes. Land first.

Items 2, 3, 7, 8 are isolated and safe to land in parallel after the runtime bump — separate small PRs.

Items 4, 9, 10, 11, 12 require interface or schema changes (idempotency `Store`, lock `Locker`, outbox table, AMQP publisher contract). Land them as a coordinated set per area.

Items 5, 6 sit between — add gating/migration but do not require interface changes.

## Closely-related HIGH items worth lifting into the same release window

These aren't "data loss now" but they're operational footguns that defeat the point of the CRITICAL fixes:

- CSRF default secret per-process → multi-instance breaks (existing/05)
- `clientip` default trusts ALL RFC1918 → internal IP spoofing (existing/05)
- Idempotency `WithTTL(0)` → permanent Redis lock (existing/08)
- `ComputeCache` WaitGroup race → panic + nondeterministic shutdown (existing/08)
- `MemoryCache` default unbounded → OOM by attacker-controlled keys (existing/08)
- `retry.Loop` restarts after `nil` error → graceful workers loop forever (existing/14)

Bundle them into the same phase-1 release as the CRITICALs, ideally behind the same `app.WithProductionDefaults()` switch ([new/19](new/19-app-production-defaults.md)).
