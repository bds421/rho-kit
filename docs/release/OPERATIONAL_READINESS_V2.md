# rho-kit v2 Operational Readiness Review

Baseline: commit `b108d39` (`feat: add credential rotation hooks`).

This file is the release-facing operational review checklist for v2.0.0. It is
separate from the API freeze because an API can be type-safe and still be hard
to operate. Every module in `go.work` must appear in the module coverage matrix
so future reviews cannot skip a package silently.

## Review Standard

For each production surface, check whether operators can answer these questions
before the API freezes:

| Area | Release question |
|---|---|
| Credential and key rotation | Can secrets, tokens, passwords, KMS keys, and signing keys rotate with overlap or provider refresh? |
| TLS material rotation | Are certificate and CA reload or rolling-restart requirements explicit? |
| Startup and configuration | Are missing, weak, plaintext, partial, or hanging configuration paths rejected or bounded? |
| Shutdown and draining | Does cancellation stop listeners first, then drain workers, then close dependencies within bounded time? |
| Backpressure and bounded work | Are queues, publishers, caches, uploads, health probes, and retries bounded by size, time, or concurrency? |
| Observability and metric contracts | Are labels stable and low-cardinality, and are dashboards/runbooks updated with metric changes? |
| Health and readiness | Do dependency checks fail closed where critical, avoid leaking topology, and time out? |
| Migrations and rollback | Are schema, queue, storage, and release-level migrations replayable or documented with rollback/stop conditions? |
| Dependency and runtime gates | Are heavy SDKs isolated, publishability checked, Docker gates recorded, and release rehearsal current? |

## Credential And Key Rotation

Current status: pass with follow-up tightening in this review. The rotation
matrix lives in [../ai/credential-rotation.md](../ai/credential-rotation.md).
The new follow-up from this operational pass is that context-aware runtime
providers must receive bounded contexts on startup and reconnect paths. AMQP URL
providers and SFTP password providers now have explicit timeout contracts.

## TLS Material Rotation

Current status: live in-process reload is available as an opt-in for v2.0.0.

Two contracts ship side-by-side:

1. **Static (default)**: `security/netutil.TLSConfig.ServerTLS` /
   `ClientTLS` load TLS material once at startup. Services rotate by
   updating the mounted files and rolling replicas. This remains the
   default golden path; no change for services that prefer
   rolling-restart semantics.
2. **Reloading (opt-in)**: `Builder.ReloadingTLS(opts...)` wires
   `netutil.FilesCertificateSource` into the public server (via
   `ReloadingServerTLS`), threads the same source into the default
   outbound HTTP client (via `ReloadingClientTLS`), and registers the
   poller's `Close` on the lifecycle Runner. Trigger reloads via
   `WithReloadInterval` (background polling) or
   `FilesCertificateSource.Reload` from a SIGHUP handler. Services that
   build their own `*tls.Config` (broker adapters, gRPC dial loops)
   read `Infrastructure.TLSCertSource` and pass it through the same
   `Reloading*TLS` helpers so the whole service shares one reload
   cycle. `ReloadingClientTLS` fails closed with
   `netutil.ErrServerNameRequired` if the caller dials without setting
   `tls.Config.ServerName`.

## Startup And Configuration

Current status: pass for current gates. Startup-sensitive providers must be
context-aware and bounded. Builder module initialization is already wrapped by
the startup timeout; adapter-specific connection loops must not bypass bounded
provider calls.

## Shutdown And Draining

Current status: pass by `runtime/lifecycle` and Builder review. Public servers
stop before gRPC and background components, `BeforeStop` runs while dependencies
are still live, and component stop calls receive bounded per-component budgets
with salvage calls after the global deadline.

## Backpressure And Bounded Work

Current status: pass for reviewed surfaces. Message size limits, upload limits,
health check timeouts, cron job timeouts, outbox publish timeouts, Redis pool
monitoring, and buffered publisher bounds are part of the operational contract.
Any new unbounded loop, queue, body read, retry, or goroutine needs a release
blocker review before v2.0.0.

## Observability And Metric Contracts

Current status: pass for the frozen v2 Prometheus contract. Metric families and
labels are scoped in [API_FREEZE_V2.md](API_FREEZE_V2.md), dashboards and
Prometheus rules are validated by `make check-dashboards`, and runbook URLs
point to `docs/ai/runbooks`.

## Health And Readiness

Current status: pass for current reviewed modules. Health check names use safe
or opaque labels, dependency checks have timeouts, and Builder keeps the
internal health/metrics listener alive during component drain.

## Migrations And Rollback

Current status: pass for documented release preparation. Database migration
helpers, outbox/idempotency store migrations, release-level tag sequencing, and
rollback stop conditions are documented in the release runbook and migration
guide. Final release still requires the full Docker-backed integration gate.

## Dependency And Runtime Gates

Current status: pass for non-Docker gates on the latest committed tree before
this review started. Final tagging still requires current `make
release-candidate` or equivalent evidence, including Docker integration,
coverage, race, dashboard validation, publishability, and rehearsal.

## Findings

| ID | Severity | Surface | Status | Finding |
|---|---|---|---|---|
| OR-001 | HIGH | AMQP and SFTP rotating credential providers | Fixed in this review | Provider callbacks were context-aware but did not have explicit provider-timeout contracts on every startup/reconnect path. |
| OR-002 | MEDIUM | TLS material | Documented contract | Static load is the default; live in-process reload is opt-in via `Builder.ReloadingTLS` (threads `FilesCertificateSource` into server, default HTTP client, and `Infrastructure.TLSCertSource` for adapter consumers). Rolling-restart remains supported for services that prefer it. |
| OR-003 | MEDIUM | Full release evidence | Open release gate | Docker integration and full `make release-candidate` remain final pre-tag gates, not covered by the targeted operational check. |

## Module Coverage Matrix

| Module | Class | Operational review focus |
|---|---|---|
| `github.com/bds421/rho-kit/app/v2` | Runtime | Builder startup, TLS, module lifecycle, health, shutdown order, credential provider wiring. |
| `github.com/bds421/rho-kit/app/amqp/v2` | Adapter | AMQP module wiring: TLS reload, credential rotation provider with bounded timeout, declarative topology, reconnect, graceful drain on Stop. |
| `github.com/bds421/rho-kit/app/grpc/v2` | Adapter | gRPC server module wiring: keep-alive tuning, TLS reload, mTLS identity, interceptor stack, graceful stop on shutdown. |
| `github.com/bds421/rho-kit/app/nats/v2` | Adapter | NATS module wiring: TLS reload, credential/token rotation provider with bounded timeout, JetStream consumer registration, drain on Stop. |
| `github.com/bds421/rho-kit/app/postgres/v2` | Adapter | Postgres pool module wiring: password provider with `Pool.Reset` on rotation, TLS reload, statement timeout, pool limits, graceful close. |
| `github.com/bds421/rho-kit/app/redis/v2` | Adapter | Redis client module wiring: go-redis credential provider, TLS reload, pool sizing, pipelined health, close on shutdown. |
| `github.com/bds421/rho-kit/app/tracing/v2` | Adapter | OpenTelemetry tracing module wiring: OTLP exporter, sampler defaults, head-based sampling, shutdown flush with deadline. |
| `github.com/bds421/rho-kit/app/actionlog/v2` | Adapter | Action-log builder wiring: pluggable storage backend selection, redaction toggle, drain on shutdown. |
| `github.com/bds421/rho-kit/app/approval/v2` | Adapter | Approval-flow builder wiring: storage backend, retention policy, audit-log fan-out, shutdown drain. |
| `github.com/bds421/rho-kit/app/auditlog/v2` | Adapter | Audit-log builder wiring: backend selection, batched flush, structured redaction, shutdown drain. |
| `github.com/bds421/rho-kit/app/authz/v2` | Adapter | Authz module wiring: policy provider plumbing, fail-closed defaults, audit fan-out, shutdown order. |
| `github.com/bds421/rho-kit/app/budget/v2` | Adapter | Budget module wiring: backend selection, refund handler attachment, lifecycle drain. |
| `github.com/bds421/rho-kit/app/cron/v2` | Adapter | Cron module wiring: scheduler attachment to lifecycle, job registry, shutdown drain. |
| `github.com/bds421/rho-kit/app/eventbus/v2` | Adapter | Eventbus module wiring: worker pool sizing, async handler registration, shutdown drain. |
| `github.com/bds421/rho-kit/app/flags/v2` | Adapter | Feature flags module wiring: provider attachment, refresh cadence, fail-closed defaults. |
| `github.com/bds421/rho-kit/app/http/v2` | Adapter | HTTP server module wiring: middleware stack composition, lifecycle attachment, shutdown drain. |
| `github.com/bds421/rho-kit/app/jwt/v2` | Adapter | JWT module wiring: provider backend selection, JWKS refresh cadence, lifecycle attachment. |
| `github.com/bds421/rho-kit/app/leader/v2` | Adapter | Leader-election module wiring: elector backend selection, callback registration, shutdown release. |
| `github.com/bds421/rho-kit/app/paseto/v2` | Adapter | PASETO module wiring: key store attachment, refresh cadence, lifecycle drain. |
| `github.com/bds421/rho-kit/app/ratelimit/v2` | Adapter | Rate-limit module wiring: limiter backend selection, key extractor, lifecycle attachment. |
| `github.com/bds421/rho-kit/app/signedrequest/v2` | Adapter | Signed-request module wiring: key store attachment, refresh cadence, lifecycle drain. |
| `github.com/bds421/rho-kit/app/slo/v2` | Adapter | SLO module wiring: registry attachment, burn-rate alert fan-out, lifecycle attachment. |
| `github.com/bds421/rho-kit/app/storage/v2` | Adapter | Storage module wiring: backend selection, upload validator stack, lifecycle attachment. |
| `github.com/bds421/rho-kit/app/tenant/v2` | Adapter | Tenant module wiring: tenant extractor attachment, fail-closed defaults, lifecycle drain. |
| `github.com/bds421/rho-kit/authz/v2` | Runtime | Policy defaults, fail-closed authorization, low-cardinality audit behavior. |
| `github.com/bds421/rho-kit/authz/openfga/v2` | Adapter | External authz dependency configuration, client deadlines, optional dependency isolation. |
| `github.com/bds421/rho-kit/cmd/kit-doctor/v2` | Tool | Static operational/security checks for downstream services. |
| `github.com/bds421/rho-kit/cmd/kit-migrate/v2` | Tool | Migration execution safety and rollback-oriented failure reporting. |
| `github.com/bds421/rho-kit/cmd/kit-new/v2` | Tool | Generated service defaults, TLS, Redis, idempotency, and golden-path compile evidence. |
| `github.com/bds421/rho-kit/cmd/kit-verify/v2` | Tool | Runtime endpoint verification and operator diagnostics. |
| `github.com/bds421/rho-kit/core/v2` | Runtime | Config, redaction, TLS cloning, validation, typed errors, tenant key safety. |
| `github.com/bds421/rho-kit/crypto/v2` | Runtime | Signing/encryption key rotation, envelope metadata, secret zeroing, bounded crypto inputs. |
| `github.com/bds421/rho-kit/crypto/encrypt/integrationtest/v2` | Integration helper | Docker-backed encryption test coverage only. |
| `github.com/bds421/rho-kit/crypto/envelope/awskms/v2` | Adapter | KMS key confinement, cloud credential providers, timeout propagation. |
| `github.com/bds421/rho-kit/crypto/envelope/azurekeyvault/v2` | Adapter | Key Vault key confinement, Azure credential providers, timeout propagation. |
| `github.com/bds421/rho-kit/crypto/envelope/gcpkms/v2` | Adapter | KMS key confinement, GCP credential providers, timeout propagation. |
| `github.com/bds421/rho-kit/crypto/envelope/vaulttransit/v2` | Adapter | Vault Transit key confinement, Vault credential handling, timeout propagation. |
| `github.com/bds421/rho-kit/data/v2` | Runtime | Memory stores, budgets, cache compute, idempotency, queue contracts, bounded defaults. |
| `github.com/bds421/rho-kit/data/actionlog/postgres/v2` | Adapter | Durable append-only log migrations, key rotation, verification, cleanup. |
| `github.com/bds421/rho-kit/data/actionlog/postgres/integrationtest/v2` | Integration helper | Postgres action-log integration coverage only. |
| `github.com/bds421/rho-kit/data/approval/postgres/v2` | Adapter | Durable approval workflow migrations, replay safety, idempotent state transitions. |
| `github.com/bds421/rho-kit/data/approval/postgres/integrationtest/v2` | Integration helper | Postgres approval integration coverage only. |
| `github.com/bds421/rho-kit/data/budget/redis/v2` | Adapter | Redis script atomicity, retry-after accuracy, tenant key safety, Redis dependency readiness. |
| `github.com/bds421/rho-kit/data/budget/redis/integrationtest/v2` | Integration helper | Redis budget integration coverage only. |
| `github.com/bds421/rho-kit/data/cache/rediscache/v2` | Adapter | Redis cache size limits, degraded behavior, Redis credential provider delegation. |
| `github.com/bds421/rho-kit/data/cache/rediscache/integrationtest/v2` | Integration helper | Redis cache integration coverage only. |
| `github.com/bds421/rho-kit/data/idempotency/pgstore/v2` | Adapter | Postgres idempotency migrations, lock ownership, replay safety. |
| `github.com/bds421/rho-kit/data/idempotency/pgstore/integrationtest/v2` | Integration helper | Postgres idempotency integration coverage only. |
| `github.com/bds421/rho-kit/data/idempotency/redisstore/v2` | Adapter | Redis lock ownership, TTL behavior, retry/cancellation handling. |
| `github.com/bds421/rho-kit/data/idempotency/redisstore/integrationtest/v2` | Integration helper | Redis idempotency-store integration coverage only. |
| `github.com/bds421/rho-kit/data/lock/pgadvisory/v2` | Adapter | Session health detection, lock release, split-brain prevention. |
| `github.com/bds421/rho-kit/data/lock/pgadvisory/integrationtest/v2` | Integration helper | Postgres advisory-lock integration coverage only. |
| `github.com/bds421/rho-kit/data/lock/redislock/v2` | Adapter | Lease extension, release ownership, Redis outage behavior. |
| `github.com/bds421/rho-kit/data/lock/redislock/integrationtest/v2` | Integration helper | Redis distributed-lock integration coverage only. |
| `github.com/bds421/rho-kit/data/queue/redisqueue/v2` | Adapter | Heartbeats, reaper behavior, processing-list ownership, retry/DLX semantics. |
| `github.com/bds421/rho-kit/data/queue/redisqueue/integrationtest/v2` | Integration helper | Redis queue integration coverage only. |
| `github.com/bds421/rho-kit/data/queue/riverqueue/v2` | Adapter | Postgres-backed queue lifecycle, migrations, shutdown behavior. |
| `github.com/bds421/rho-kit/data/queue/riverqueue/integrationtest/v2` | Integration helper | River/Postgres queue integration coverage only. |
| `github.com/bds421/rho-kit/data/ratelimit/redis/v2` | Adapter | Redis GCRA atomicity, retry-after precision, Redis outage behavior. |
| `github.com/bds421/rho-kit/data/ratelimit/redis/integrationtest/v2` | Integration helper | Redis rate-limit integration coverage only. |
| `github.com/bds421/rho-kit/data/stream/redisstream/v2` | Adapter | Consumer group ownership, pending/dead-letter metrics, Redis outage behavior. |
| `github.com/bds421/rho-kit/data/stream/redisstream/integrationtest/v2` | Integration helper | Redis stream integration coverage only. |
| `github.com/bds421/rho-kit/data/tenant/v2` | Runtime | Per-tenant data-isolation primitives: Scope value type, WhereClause SQL helper, tenant-prefixed cache/idempotency keys. |
| `github.com/bds421/rho-kit/examples/agentic-service/v2` | Example | Golden-path smoke coverage and generated-service operational defaults. |
| `github.com/bds421/rho-kit/flags/v2` | Runtime | Fallback behavior, config validation, test/local provider boundaries. |
| `github.com/bds421/rho-kit/grpcx/v2` | Runtime | gRPC server defaults, health, interceptors, deadlines, mTLS identity. |
| `github.com/bds421/rho-kit/httpx/v2` | Runtime | HTTP server/client timeouts, middleware order, signing, CSRF, metrics, request budgets. |
| `github.com/bds421/rho-kit/httpx/middleware/signedrequest/redis/v2` | Adapter | Redis nonce TTL, cancellation handling, Redis outage behavior. |
| `github.com/bds421/rho-kit/httpx/websocket/v2` | Adapter | WebSocket upgrade ergonomics, per-message read limits, idempotent close, handler panic recovery, redacted error surface, metric cardinality bound. |
| `github.com/bds421/rho-kit/httpx/websocket/integrationtest/v2` | Integration helper | Real-listener WebSocket round-trip coverage only. |
| `github.com/bds421/rho-kit/infra/v2` | Runtime | Infrastructure interfaces, sentinels, storage/messaging contracts, release-stable errors. |
| `github.com/bds421/rho-kit/infra/leaderelection/k8slease/v2` | Adapter | Lease renew/retry deadlines, identity uniqueness, callback drain, API-server outage behavior. |
| `github.com/bds421/rho-kit/infra/leaderelection/k8slease/integrationtest/v2` | Integration helper | Fake-clientset leader-election integration coverage only. |
| `github.com/bds421/rho-kit/infra/leaderelection/pgadvisory/v2` | Adapter | Leadership health, callback drain, Postgres session loss behavior. |
| `github.com/bds421/rho-kit/infra/leaderelection/pgadvisory/integrationtest/v2` | Integration helper | Postgres advisory leader-election integration coverage only. |
| `github.com/bds421/rho-kit/infra/leaderelection/redislock/v2` | Adapter | Leadership lease extension, callback drain, Redis outage behavior. |
| `github.com/bds421/rho-kit/infra/leaderelection/redislock/integrationtest/v2` | Integration helper | Redis leader-election integration coverage only. |
| `github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2` | Adapter | AMQP reconnect, URL provider rotation, provider timeout, metrics, consumer ack/nack semantics. |
| `github.com/bds421/rho-kit/infra/messaging/amqpbackend/debughttp/v2` | Adapter | Debug endpoint access control and safe broker diagnostics. |
| `github.com/bds421/rho-kit/infra/messaging/amqpbackend/integrationtest/v2` | Integration helper | RabbitMQ integration coverage only. |
| `github.com/bds421/rho-kit/infra/messaging/kafkabackend/v2` | Adapter | Kafka TLS / SASL config, consumer-group rebalance, offset-commit semantics, poison-pill discard, retention-driven replay. |
| `github.com/bds421/rho-kit/infra/messaging/kafkabackend/integrationtest/v2` | Integration helper | Kafka integration coverage only. |
| `github.com/bds421/rho-kit/infra/messaging/natsbackend/v2` | Adapter | NATS auth providers, drain, JetStream metrics, stream/consumer setup. |
| `github.com/bds421/rho-kit/infra/messaging/natsbackend/integrationtest/v2` | Integration helper | NATS integration coverage only. |
| `github.com/bds421/rho-kit/infra/messaging/redisbackend/v2` | Adapter | Redis Streams direct messaging, size limits, pending/dead-letter behavior. |
| `github.com/bds421/rho-kit/infra/messaging/redisbackend/integrationtest/v2` | Integration helper | Redis Streams messaging integration coverage only. |
| `github.com/bds421/rho-kit/infra/redis/v2` | Adapter | Redis credential providers, TLS, health loop, reconnect callback bounds. |
| `github.com/bds421/rho-kit/infra/redis/redistest/v2` | Integration helper | Redis Testcontainers helper coverage only. |
| `github.com/bds421/rho-kit/infra/sqldb/dbtest/v2` | Integration helper | Postgres Testcontainers helper coverage only. |
| `github.com/bds421/rho-kit/infra/sqldb/pgx/v2` | Adapter | Password provider rotation, pool reset, TLS/sslmode, migrations, COPY helper. |
| `github.com/bds421/rho-kit/infra/sqldb/pgx/integrationtest/v2` | Integration helper | pgx/Postgres integration coverage only. |
| `github.com/bds421/rho-kit/infra/storage/azurebackend/v2` | Adapter | Azure credentials, account-key static path, token credential path, storage metrics. |
| `github.com/bds421/rho-kit/infra/storage/gcsbackend/v2` | Adapter | ADC/client options, storage metrics, operation cancellation. |
| `github.com/bds421/rho-kit/infra/storage/s3backend/v2` | Adapter | AWS credential providers/default chain, static key validation, storage metrics. |
| `github.com/bds421/rho-kit/infra/storage/sftpbackend/v2` | Adapter | SFTP password provider rotation, provider timeout, host key validation, reconnect cleanup. |
| `github.com/bds421/rho-kit/infra/storage/storagehttp/uploadsec/clamav/v2` | Adapter | Scanner fail-closed behavior, temp-file cleanup, ClamAV outage readiness. |
| `github.com/bds421/rho-kit/infra/storage/storagetest/v2` | Integration helper | Storage backend compliance coverage only. |
| `github.com/bds421/rho-kit/infra/outbox/postgres/v2` | Adapter | Postgres outbox Store: transactional Insert via `WithTx`, `FOR UPDATE SKIP LOCKED` claim path, status-guarded `ErrStaleState` transitions, partial-index retention sweeps, stale-recovery for crashed relays. Migrations ship via `cmd/kit-migrate`. |
| `github.com/bds421/rho-kit/infra/outbox/postgres/integrationtest/v2` | Integration helper | Outbox postgres integration coverage only (tx atomicity, SKIP LOCKED, retention, stale recovery). |
| `github.com/bds421/rho-kit/io/v2` | Runtime | Atomic file writes, progress accounting, file cleanup behavior. |
| `github.com/bds421/rho-kit/observability/v2` | Runtime | Health, metrics, dashboards, runbooks, pprof, tracing, audit logs. |
| `github.com/bds421/rho-kit/observability/auditlog/postgres/v2` | Adapter | Postgres audit-log Store: tamper-evident chain via `pg_advisory_xact_lock` + tail `FOR UPDATE`, append-order verification through `seq BIGSERIAL`, signed-cursor pagination. Migrations ship via `cmd/kit-migrate`. |
| `github.com/bds421/rho-kit/observability/auditlog/postgres/integrationtest/v2` | Integration helper | Audit-log postgres integration coverage only (chain integrity, tamper / deletion detection, concurrent appends, backfilled-timestamp safety). |
| `github.com/bds421/rho-kit/resilience/v2` | Runtime | Retry/circuit-breaker defaults, context/error precedence, bounded retries. |
| `github.com/bds421/rho-kit/runtime/v2` | Runtime | Lifecycle, cron, eventbus, batchworker, fanout, cancellation and drain behavior; saga compensable-workflow primitive (roll-forward + reverse-order best-effort rollback). |
| `github.com/bds421/rho-kit/runtime/temporal/v2` | Adapter | Temporal dependency isolation, workflow scaffold, operational dependency caveat. |
| `github.com/bds421/rho-kit/security/v2` | Runtime | JWT refresh, CSRF rotation, mTLS identity, SSRF guard, ASVS catalog. |
| `github.com/bds421/rho-kit/testing/integrationtest/v2` | Integration helper | Umbrella module that will host the kit's consolidated Docker-backed integration tests (Wave 154 scaffold; per-package suites move in over follow-up commits). |
| `github.com/bds421/rho-kit/testing/kittest/v2` | Integration helper | Re-export umbrella over dbtest/redistest/storagetest/rabbitmqtest; no runtime surface. |

## Check Command

Run the coverage check before tagging:

```bash
make check-operational-readiness
```

The check verifies that this document exists, contains all required operational
sections, and lists every module from `go.work` in the module coverage matrix.
