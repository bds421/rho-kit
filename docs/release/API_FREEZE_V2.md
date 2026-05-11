# rho-kit v2 Public API Freeze

Baseline: commit `bfb475f` (`chore: harden rho-kit for v2 release`).

This document freezes the module-level public surface for v2.0.0. "Keep" means
the module path and exported API are considered release candidates: changes
after this point need a concrete RC-blocker rationale, tests, and a release-note
entry. "Adapter" means optional dependency weight is intentionally isolated in
that module. "Integration test helper" means the module is not a production
runtime dependency and may depend on Docker/Testcontainers.

No module is approved for removal or rename before v2.0.0. New modules are not
allowed unless the dependency-boundary and allowlist checks are updated in the
same change.

## Core Service Surface

| Module | Decision | Rename/remove decision | Freeze notes |
|---|---|---|---|
| `github.com/bds421/rho-kit/app/v2` | Keep | No rename/remove | Golden-path service builder. New `With*` methods are frozen for v2.0.0. |
| `github.com/bds421/rho-kit/core/v2` | Keep | No rename/remove | Shared low-dependency primitives: config, typed errors, tenant IDs, redaction, secrets, validation, tls clone helpers. |
| `github.com/bds421/rho-kit/httpx/v2` | Keep | No rename/remove | HTTP server/client defaults, JSON helpers, middleware, authz bridge, MCP, pagination, signing, redirect safety. |
| `github.com/bds421/rho-kit/grpcx/v2` | Keep | No rename/remove | gRPC server defaults, interceptors, RED metrics, auth, health, deadlines. |
| `github.com/bds421/rho-kit/security/v2` | Keep | No rename/remove | JWT, CSRF, mTLS/SSRF helpers, ASVS metadata, revocation, mTLS identity. |
| `github.com/bds421/rho-kit/crypto/v2` | Keep | No rename/remove | Encryption, envelope, PASETO, password hashing, signing. |
| `github.com/bds421/rho-kit/runtime/v2` | Keep | No rename/remove | Lifecycle, concurrency, batchworker, cron, eventbus. |
| `github.com/bds421/rho-kit/resilience/v2` | Keep | No rename/remove | Retry and circuit breaker primitives. |
| `github.com/bds421/rho-kit/observability/v2` | Keep | No rename/remove | Health, log attributes, logging, pprof, Prometheus utilities, RED metrics, runtime metrics, SLO, tracing. |
| `github.com/bds421/rho-kit/io/v2` | Keep | No rename/remove | Atomic file and progress helpers. |
| `github.com/bds421/rho-kit/flags/v2` | Keep | No rename/remove | Feature flag interfaces and in-memory provider. |
| `github.com/bds421/rho-kit/authz/v2` | Keep | No rename/remove | Authorization contracts and memory policy. |

## Data And Infrastructure Runtime Modules

| Module | Decision | Rename/remove decision | Freeze notes |
|---|---|---|---|
| `github.com/bds421/rho-kit/data/v2` | Keep | No rename/remove | Data contracts plus memory implementations for action log, approval, budget, cache, idempotency, queue, stream, locks, rate limits. |
| `github.com/bds421/rho-kit/infra/v2` | Keep | No rename/remove | Infrastructure contracts: messaging, Redis, SQL DB, storage, outbox, leader election. |
| `github.com/bds421/rho-kit/data/idempotency/pgstore/v2` | Adapter | No rename/remove | Postgres idempotency store; owns its migration surface. |
| `github.com/bds421/rho-kit/data/idempotency/redisstore/v2` | Adapter | No rename/remove | Redis idempotency store. |
| `github.com/bds421/rho-kit/data/cache/rediscache/v2` | Adapter | No rename/remove | Redis cache backend with bulk limits and degraded-mode wrapper. |
| `github.com/bds421/rho-kit/data/budget/redis/v2` | Adapter | No rename/remove | Redis-backed tenant budget ledger. |
| `github.com/bds421/rho-kit/data/actionlog/postgres/v2` | Adapter | No rename/remove | Postgres signed action-log store and migrations. |
| `github.com/bds421/rho-kit/data/approval/postgres/v2` | Adapter | No rename/remove | Postgres approval workflow store and migrations. |
| `github.com/bds421/rho-kit/data/lock/pgadvisory/v2` | Adapter | No rename/remove | Postgres advisory-lock implementation. |
| `github.com/bds421/rho-kit/data/lock/redislock/v2` | Adapter | No rename/remove | Redis lock implementation. |
| `github.com/bds421/rho-kit/data/queue/redisqueue/v2` | Adapter | No rename/remove | Redis list-backed queue. |
| `github.com/bds421/rho-kit/data/queue/riverqueue/v2` | Adapter | No rename/remove | River/Postgres queue adapter. |
| `github.com/bds421/rho-kit/data/ratelimit/redis/v2` | Adapter | No rename/remove | Redis GCRA distributed rate limiter. |
| `github.com/bds421/rho-kit/data/stream/redisstream/v2` | Adapter | No rename/remove | Redis Streams producer/consumer. |
| `github.com/bds421/rho-kit/infra/redis/v2` | Adapter | No rename/remove | Redis connection/config/health helpers. |
| `github.com/bds421/rho-kit/infra/sqldb/pgx/v2` | Adapter | No rename/remove | pgx pool, migrations, COPY helper. |
| `github.com/bds421/rho-kit/infra/sqldb/dbtest/v2` | Integration test helper | No rename/remove | Docker-backed Postgres test helper, not production runtime. |
| `github.com/bds421/rho-kit/infra/leaderelection/pgadvisory/v2` | Adapter | No rename/remove | Leader election using Postgres advisory locks. |
| `github.com/bds421/rho-kit/infra/leaderelection/redislock/v2` | Adapter | No rename/remove | Leader election using Redis locks. |

## Messaging, Storage, And Optional SDK Adapters

| Module | Decision | Rename/remove decision | Freeze notes |
|---|---|---|---|
| `github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2` | Adapter | No rename/remove | RabbitMQ/AMQP publisher and consumer. |
| `github.com/bds421/rho-kit/infra/messaging/amqpbackend/debughttp/v2` | Adapter | No rename/remove | Guarded AMQP debug HTTP helpers. |
| `github.com/bds421/rho-kit/infra/messaging/natsbackend/v2` | Adapter | No rename/remove | NATS JetStream backend, dependency isolated here. |
| `github.com/bds421/rho-kit/infra/messaging/redisbackend/v2` | Adapter | No rename/remove | Messaging bridge over Redis streams. |
| `github.com/bds421/rho-kit/infra/storage/azurebackend/v2` | Adapter | No rename/remove | Azure Blob storage backend, dependency isolated here. |
| `github.com/bds421/rho-kit/infra/storage/gcsbackend/v2` | Adapter | No rename/remove | Google Cloud Storage backend, dependency isolated here. |
| `github.com/bds421/rho-kit/infra/storage/s3backend/v2` | Adapter | No rename/remove | S3-compatible storage backend, dependency isolated here. |
| `github.com/bds421/rho-kit/infra/storage/sftpbackend/v2` | Adapter | No rename/remove | SFTP storage backend, dependency isolated here. |
| `github.com/bds421/rho-kit/infra/storage/storagehttp/uploadsec/clamav/v2` | Adapter | No rename/remove | ClamAV scanner adapter, dependency isolated here. |
| `github.com/bds421/rho-kit/infra/storage/storagetest/v2` | Integration test helper | No rename/remove | Storage compliance suites and Docker-backed local helpers. |
| `github.com/bds421/rho-kit/authz/openfga/v2` | Adapter | No rename/remove | OpenFGA adapter, dependency isolated here. |
| `github.com/bds421/rho-kit/crypto/envelope/awskms/v2` | Adapter | No rename/remove | AWS KMS envelope KEK adapter. |
| `github.com/bds421/rho-kit/crypto/envelope/gcpkms/v2` | Adapter | No rename/remove | Google Cloud KMS envelope KEK adapter. |
| `github.com/bds421/rho-kit/runtime/temporal/v2` | Adapter | No rename/remove | Temporal helpers, dependency isolated here. |
| `github.com/bds421/rho-kit/httpx/middleware/signedrequest/redis/v2` | Adapter | No rename/remove | Redis nonce store for signed-request middleware. |

## Integration Test Modules

| Module | Decision | Rename/remove decision | Freeze notes |
|---|---|---|---|
| `github.com/bds421/rho-kit/crypto/encrypt/integrationtest/v2` | Integration test helper | No rename/remove | Docker-backed crypto/encrypt integration tests. |
| `github.com/bds421/rho-kit/data/actionlog/postgres/integrationtest/v2` | Integration test helper | No rename/remove | Postgres action-log integration tests. |
| `github.com/bds421/rho-kit/data/approval/postgres/integrationtest/v2` | Integration test helper | No rename/remove | Postgres approval integration tests. |
| `github.com/bds421/rho-kit/data/cache/rediscache/integrationtest/v2` | Integration test helper | No rename/remove | Redis cache integration tests. |
| `github.com/bds421/rho-kit/data/idempotency/pgstore/integrationtest/v2` | Integration test helper | No rename/remove | Postgres idempotency integration tests. |
| `github.com/bds421/rho-kit/data/lock/pgadvisory/integrationtest/v2` | Integration test helper | No rename/remove | Postgres advisory-lock integration tests. |
| `github.com/bds421/rho-kit/data/queue/redisqueue/integrationtest/v2` | Integration test helper | No rename/remove | Redis queue integration tests. |
| `github.com/bds421/rho-kit/data/queue/riverqueue/integrationtest/v2` | Integration test helper | No rename/remove | River/Postgres queue integration tests. |
| `github.com/bds421/rho-kit/data/stream/redisstream/integrationtest/v2` | Integration test helper | No rename/remove | Redis stream integration tests. |
| `github.com/bds421/rho-kit/infra/messaging/amqpbackend/integrationtest/v2` | Integration test helper | No rename/remove | RabbitMQ integration tests and `rabbitmqtest` helper. |
| `github.com/bds421/rho-kit/infra/messaging/natsbackend/integrationtest/v2` | Integration test helper | No rename/remove | NATS integration tests. |
| `github.com/bds421/rho-kit/infra/redis/redistest/v2` | Integration test helper | No rename/remove | Redis Testcontainers helper. |
| `github.com/bds421/rho-kit/infra/sqldb/pgx/integrationtest/v2` | Integration test helper | No rename/remove | pgx/Postgres integration tests. |

## Commands And Examples

| Module | Decision | Rename/remove decision | Freeze notes |
|---|---|---|---|
| `github.com/bds421/rho-kit/cmd/kit-bench-gate/v2` | Keep command API | No rename/remove | Performance-regression gate CLI. |
| `github.com/bds421/rho-kit/cmd/kit-doctor/v2` | Keep command API | No rename/remove | Static service-health/security scanner CLI. |
| `github.com/bds421/rho-kit/cmd/kit-migrate/v2` | Keep command API | No rename/remove | Kit-managed DB migration CLI. |
| `github.com/bds421/rho-kit/cmd/kit-new/v2` | Keep command API | No rename/remove | Service scaffold CLI; scaffold variants are compile-tested. |
| `github.com/bds421/rho-kit/cmd/kit-verify/v2` | Keep command API | No rename/remove | Runtime endpoint verification CLI. |
| `github.com/bds421/rho-kit/examples/agentic-service/v2` | Keep example | No rename/remove | Buildable smoke example. It is not production starter code; production starts from `app.Builder` or `kit-new`. |

## Release Rules After Freeze

- Public `With*`, constructor, interface, error sentinel, and config-field changes
  require a migration note in [MIGRATION_V2.md](MIGRATION_V2.md) and a release-note
  entry in [../RELEASE_NOTES_v2.md](../RELEASE_NOTES_v2.md).
- Optional cloud/provider SDKs stay in adapter modules. Base modules (`app`,
  `core`, `httpx`, `data`, `infra`, `runtime`, `security`, `observability`) must
  not gain unreviewed heavy dependencies.
- Integration helpers stay in split modules and behind `integration` build tags.
- New product-specific abstractions are rejected unless they can be named as a
  reusable platform primitive and mapped into the package decision tree.
