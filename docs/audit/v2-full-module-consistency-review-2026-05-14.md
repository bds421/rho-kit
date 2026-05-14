# rho-kit v2.0.0 Full Module And Cross-Module Consistency Review

Date: 2026-05-14

Scope: every module listed by `go work edit -json` in the current workspace.

Method:

- Re-derived the live release surface with `go work edit -json` and `RELEASE_MODE=all make release-plan`.
- Generated module packets from tracked source, tests, docs, constructors, options, metrics, context, lifecycle, logging, TLS/credential, and limit-related symbols.
- Inspected source/tests for confirmed findings. Comments and release notes were treated as claims, not evidence.
- Ran cross-module consistency checks for migration registration, public API leaks, metric/registerer naming, storage metric semantics, redaction/logging, credential/TLS rotation, lifecycle/shutdown contracts, and docs/release inventory.

Result: 75 of 75 `go.work` modules were included. There are no intentionally skipped workspace modules in this pass. `-` in the matrix means the axis was reviewed and did not apply or had no relevant signal in that module; it does not mean "not reviewed".

Commands used as evidence:

```bash
go work edit -json
RELEASE_MODE=all make release-plan
go run ./cmd/kit-migrate list
rg -n '^func With.*Registerer|^func MetricsWithRegisterer|^func With.*Registry|prometheus\.(Registerer|Gatherer)'
rg -n 'LogAuthz|func \(l \*Logger\) Log' authz observability/auditlog --glob '*.go'
rg -n 'type .* = .*validator|validator\.Validate|go-playground/validator' --glob '*.go'
```

## Executive Findings

| ID | Severity | Area | Finding |
|---|---|---|---|
| F-001 | High | migrations/release tooling | New `observability/auditlog/postgres` migrations are embedded but not registered in `cmd/kit-migrate`, so services cannot discover/publish the schema through the kit migration tool. |
| F-002 | High | authz/auditlog API | `authz.WithAuditSink` claims `observability/auditlog.Logger` satisfies `AuditSink`, but `Logger` has no `LogAuthz` method. The documented cross-module wiring does not compile. |
| F-003 | Medium | logging/redaction | `authz.Logged` emits raw subject/resource/action values to `slog`, while the HTTP authz path redacts the same classes of values. |
| F-004 | High | operator diagnostics | `app.Main` redacts the top-level startup error down to only the concrete type, leaving failed services with no actionable start-failure message. |
| F-005 | Medium | release docs/redaction | Release notes overclaim redaction coverage: module names, internal/gRPC listener addresses, and gRPC impersonation identity fields are still logged raw in source. |
| F-006 | Medium | Prometheus storage contracts | Storage `operation_errors_total` treats not-found `Get` differently across S3, Azure, GCS, and SFTP. This should be frozen consistently before Prometheus contracts are declared stable. |
| F-007 | Medium | API freeze/docs | The new auditlog Postgres modules are in `go.work` and release-plan output but absent from API freeze docs, operational readiness docs, and the AI/user decision tree. |
| F-008 | Medium | public API shape | `core/validate.Func` is a type alias to `go-playground/validator/v10.Func`, despite comments saying callers should not depend on the third-party public type. |
| C-001 | Caveat | Prometheus API naming | Registerer options are generally options-based, but names are not fully uniform (`WithRegisterer`, `WithMetricsRegisterer`, `MetricsWithRegisterer`, `WithHTTPRegisterer`, `WithBatchRegisterer`). Decide deliberately before freezing, because this is API shape rather than runtime correctness. |
| C-002 | Caveat | auditlog Store limit | `auditlog.Logger.List` caps page limits, but public `Store.Query` implementations accept direct oversized limits. If `Store` is intended as an internal SPI behind `Logger`, document that; if it is public user API, cap/reject at the Store boundary too. |

## Confirmed Findings

### F-001: `cmd/kit-migrate` omits auditlog Postgres migrations

`observability/auditlog/postgres` embeds a migration filesystem in `migrations.go:13-14`, and the migration creates `audit_log_events`. However, `cmd/kit-migrate/main.go:22-35` imports and registers only actionlog, approval, and idempotency migrations:

```text
actionlog:
  20260507000001_create_action_log_entries.sql
approval:
  20260507000001_create_approval_requests.sql
idempotency:
  20260101000001_create_idempotency_keys.sql
  20260505000001_owner_token_and_fingerprint.sql
```

Impact: the new durable audit store looks shippable from its package tests, but the release tooling path services use to publish kit schemas cannot discover it. This is a release blocker for the new module.

Fix direction: import `observability/auditlog/postgres/v2` in `cmd/kit-migrate`, add it to `registry`, update `cmd/kit-migrate` tests, and update release docs.

### F-002: `authz.AuditSink` docs point to a non-existent `auditlog.Logger.LogAuthz`

`authz/audit.go:22-28` defines:

```go
type AuditSink interface {
    LogAuthz(ctx context.Context, event AuditEvent)
}
```

`authz/audit.go:51-55` says the concrete `observability/auditlog.Logger` satisfies the surface. Source search shows `auditlog.Logger` has `Log`, `LogE`, and `LogAction`, but no `LogAuthz`.

Impact: the advertised wiring `authz.WithAuditSink(auditLogger)` does not compile. This is exactly the kind of cross-module API mismatch that a per-package-only review misses.

Fix direction: either add `LogAuthz` to `observability/auditlog.Logger`, add an adapter type/function in one of the packages, or correct the docs and examples to require an explicit adapter.

### F-003: `authz.Logged` logs raw actor/resource values

`authz/audit.go:108-118` logs:

```go
slog.String("actor", subject)
slog.String("resource", resource)
slog.String("verb", action)
```

The HTTP auth path redacts analogous values in `httpx/middleware/auth/auth.go:382-396` and `:409-412`. The authz contract itself allows SPIFFE IDs/resource paths up to 512 bytes, so these values can contain tenant or topology data.

Impact: inconsistent redaction semantics across auth modules. The audit sink can preserve full structured values, but ordinary runtime logs should use redaction or a documented safe identifier policy.

Fix direction: use `redact.String` or `observability/logattr` for `actor` and `resource` in normal logs. Keep full values only in the explicit audit event sink.

### F-004: `app.Main` top-level startup errors are not diagnosable

`app/serviceboot.go:33-35` logs `redact.Error(err)` for the process-fatal error. `core/redact/redact.go:51-70` unwraps and renders only `"<redacted error: %T>"`.

Impact: when a service fails to start, operators get a concrete type but not the message or the failing subsystem. That is too little for a fatal startup path controlled mostly by service configuration and kit startup code.

Fix direction: introduce a startup-safe diagnostic wrapper that preserves typed kit errors and sanitized config/dependency messages, or log a safe error code/subsystem/cause chain separate from request-path redaction.

### F-005: redaction release notes are broader than the source

`docs/RELEASE_NOTES_v2.md:492-500` claims app module lifecycle, top-level application errors, gRPC/internal listener address logs, and mTLS impersonation logs avoid copying raw identifiers.

Source disagrees:

- `app/module.go:237-286` logs module names raw with `slog.String("module", m.Name())`.
- `app/builder.go:1414` logs the internal listener address raw.
- `app/grpc/grpc.go:183` and `:271` log gRPC addresses raw.
- `grpcx/interceptor/auth.go:614-641` logs `user_id` and `client_identity` raw.
- The HTTP counterpart redacts those fields in `httpx/middleware/auth/auth.go:382-396` and `:409-412`.

Impact: the release artifact says a privacy/logging class is solved when it is only partially solved. This leads reviewers and service owners to trust the wrong contract.

Fix direction: either apply the redaction consistently or narrow the release-note claim to the exact fields that are actually redacted.

### F-006: storage error metrics do not have one not-found contract

The storage dashboards alert from `storage_(s3|gcs|azure|sftp)_operation_errors_total` (`observability/dashboards/grafana/storage.json:55`). Metric help describes these as operation errors (`infra/storage/azurebackend/metrics.go:59-65`, analogous in the other backends).

Observed `Get` behavior:

- S3 normalizes not-found before metric observation: `s3backend/s3.go:341-347`, `s3MetricErr` at `:435-440`.
- Azure records raw `BlobNotFound` before translating it: `azurebackend/azure.go:295-300`.
- GCS records raw `ErrObjectNotExist` before translating it: `gcsbackend/gcs.go:228-233`.
- SFTP records raw not-exist before translating it: `sftpbackend/sftp.go:644-652`; `sftpMetricErr` exists at `:851-856` but is not used for `Get`.

`Delete` and `Exists` already normalize expected not-found in Azure/GCS/SFTP, so the inconsistency is specifically the `Get` path.

Impact: dashboards and alerts mean different things by provider. A cache-miss/object-miss workload can inflate Azure/GCS/SFTP error rates but not S3.

Fix direction: choose a contract before freezing. Recommended: expected not-found should not increment `operation_errors_total` for `Get`, `Delete`, or `Exists`; expose a separate not-found counter only if operators need miss rates.

### F-007: new auditlog Postgres modules are missing from release/API docs

`RELEASE_MODE=all make release-plan` reports 75 workspace modules and includes:

- `observability/auditlog/postgres`
- `observability/auditlog/postgres/integrationtest`

Docs are stale:

- `AGENTS.md:193` lists only `observability/auditlog`.
- `docs/ai/observability.md:3` and `:114-118` mention only the base package and say production services should provide a durable store, but do not point to the new Postgres store.
- `docs/ai/bootstrap.md:300-306` still shows `auditlog.NewMemoryStore()` with a comment to replace it in production.
- `docs/release/API_FREEZE_V2.md:52-97` omits `observability/auditlog/postgres/v2`.
- `docs/release/API_FREEZE_V2.md:98-115` omits `observability/auditlog/postgres/integrationtest/v2`.
- `docs/release/OPERATIONAL_READINESS_V2.md:183` lists only the base observability module.

Impact: the actual release surface and the freeze artifacts disagree.

Fix direction: add both modules to API freeze and operational readiness docs, update AGENTS/AI recipes, and update the bootstrap example to show the durable Postgres store as the production path.

### F-008: `core/validate` still leaks the third-party validator function type

`core/validate/validate.go:21-24` says callers should not depend on the third-party type, but implements:

```go
type Func = validator.Func
```

`Validator.RegisterValidation` and package-level `RegisterValidation` expose that alias.

Impact: the public API is still tied to `go-playground/validator/v10` custom-function shape. If v2 aims to freeze a kit surface, this is an API decision to make deliberately.

Fix direction: either accept/document the third-party type as part of the v2 public API, or introduce a kit-owned function/context wrapper before the freeze.

## Cross-Module Consistency Table

| Axis | Status | Evidence |
|---|---|---|
| Workspace inventory | Issue | Live release-plan reports 75 modules. New auditlog Postgres modules exist in `go.work` but are not fully reflected in docs/tooling. |
| Migration discovery | Issue | `cmd/kit-migrate` lists actionlog, approval, idempotency; auditlog Postgres is absent. |
| Authz to auditlog integration | Issue | `authz.AuditSink` requires `LogAuthz`; `auditlog.Logger` does not implement it. |
| Logging/redaction | Issue | HTTP authz redacts identity/path fields; `authz.Logged` and gRPC impersonation logs still emit raw identities. |
| Fatal startup diagnostics | Issue | `app.Main` emits only redacted concrete error type. |
| Storage metrics contract | Issue | Provider `Get` not-found behavior differs across S3/Azure/GCS/SFTP. |
| Prometheus registerer API shape | Caveat | Metrics are options-based, but option names vary where packages have multiple metric constructors. |
| Credential/TLS rotation | Mostly coherent | Reloading TLS is wired through app, AMQP, NATS, HTTP client, and netutil; Postgres, Redis, AMQP, NATS, SFTP, and S3 expose provider-based credential hooks. |
| KMS key constraint | Clean in inspected adapters | AWS, GCP, Azure Key Vault, and Vault Transit constrain envelope key IDs before decrypt/unwrap. |
| Permission fail-closed behavior | Clean in inspected auth paths | HTTP/gRPC permission checks reject missing permission unless the trusted S2S bypass is explicitly active. |
| Lifecycle/shutdown | Mostly coherent | Prior cache close, eventbus stopped, pgadvisory extend, redisqueue heartbeat, NATS drain, and module stop patterns were inspected and no new blocker found in this pass. |
| Public third-party type exposure | Issue | `core/validate.Func` aliases `validator.Func`. |
| Release evidence sync | Issue | Release notes/API freeze/operational docs overclaim or omit current source state. |

## Module Coverage Matrix

Legend: `Y` = axis reviewed and relevant signal exists; `-` = reviewed and not applicable/no relevant signal.

| Module | Source files | Tests | Docs | API | Ctor | Defaults | Errors | Context | Stop/Close | Logs | Metrics | TLS/Creds | Limits |
|---|---:|---:|---:|---|---|---|---|---|---|---|---|---|---|
| `./app` | 15 | 12 | 1 | Y | Y | Y | Y | Y | Y | Y | Y | Y | - |
| `./app/amqp` | 2 | 1 | 1 | Y | Y | Y | Y | Y | Y | Y | Y | Y | - |
| `./app/grpc` | 2 | 1 | 1 | Y | Y | Y | Y | Y | Y | Y | - | Y | - |
| `./app/nats` | 2 | 1 | 1 | Y | Y | Y | Y | Y | Y | Y | Y | Y | - |
| `./app/postgres` | 2 | 1 | 1 | Y | Y | Y | Y | Y | Y | Y | Y | Y | - |
| `./app/redis` | 2 | 1 | 1 | Y | Y | Y | Y | Y | Y | Y | Y | Y | - |
| `./app/tracing` | 2 | 1 | 1 | Y | - | - | Y | Y | Y | Y | - | - | - |
| `./authz` | 3 | 2 | 0 | Y | Y | Y | Y | Y | - | Y | - | - | - |
| `./authz/openfga` | 1 | 1 | 0 | Y | Y | Y | Y | Y | - | Y | - | Y | Y |
| `./cmd/kit-bench-gate` | 3 | 3 | 0 | Y | - | - | Y | - | - | - | Y | - | - |
| `./cmd/kit-doctor` | 14 | 1 | 0 | Y | - | Y | Y | - | Y | Y | - | Y | Y |
| `./cmd/kit-migrate` | 1 | 1 | 0 | - | - | Y | Y | - | - | - | - | - | - |
| `./cmd/kit-new` | 2 | 1 | 0 | Y | - | Y | Y | - | - | - | - | - | - |
| `./cmd/kit-verify` | 1 | 1 | 0 | Y | - | Y | Y | - | Y | - | - | Y | - |
| `./core` | 30 | 24 | 7 | Y | Y | Y | Y | Y | Y | Y | Y | Y | Y |
| `./crypto` | 14 | 16 | 4 | Y | Y | Y | Y | Y | - | Y | Y | Y | Y |
| `./crypto/encrypt/integrationtest` | 1 | 1 | 1 | - | - | - | - | - | - | - | - | - | - |
| `./crypto/envelope/awskms` | 3 | 2 | 0 | Y | Y | Y | Y | Y | - | Y | Y | Y | - |
| `./crypto/envelope/azurekeyvault` | 2 | 1 | 0 | Y | Y | Y | Y | Y | - | Y | - | Y | - |
| `./crypto/envelope/gcpkms` | 2 | 1 | 0 | Y | Y | Y | Y | Y | - | Y | - | Y | - |
| `./crypto/envelope/vaulttransit` | 2 | 1 | 0 | Y | Y | Y | Y | Y | - | Y | - | Y | - |
| `./data` | 37 | 26 | 10 | Y | Y | Y | Y | Y | Y | Y | Y | Y | Y |
| `./data/actionlog/postgres` | 3 | 1 | 1 | Y | Y | Y | Y | Y | - | - | - | - | - |
| `./data/actionlog/postgres/integrationtest` | 1 | 1 | 1 | - | - | - | - | - | - | - | - | - | - |
| `./data/approval/postgres` | 3 | 1 | 1 | Y | Y | Y | Y | Y | - | - | - | - | Y |
| `./data/approval/postgres/integrationtest` | 1 | 1 | 1 | - | - | - | - | - | - | - | - | - | - |
| `./data/budget/redis` | 1 | 1 | 0 | Y | Y | Y | Y | Y | Y | - | - | - | - |
| `./data/cache/rediscache` | 3 | 2 | 1 | Y | Y | Y | Y | Y | - | - | Y | - | - |
| `./data/cache/rediscache/integrationtest` | 1 | 1 | 1 | - | - | - | - | - | - | - | - | - | - |
| `./data/idempotency/pgstore` | 2 | 1 | 0 | Y | Y | Y | Y | Y | - | - | - | - | - |
| `./data/idempotency/pgstore/integrationtest` | 1 | 1 | 1 | - | - | - | - | - | - | - | - | - | - |
| `./data/idempotency/redisstore` | 3 | 2 | 1 | Y | Y | Y | Y | Y | Y | - | - | Y | Y |
| `./data/lock/pgadvisory` | 1 | 1 | 0 | Y | Y | - | Y | Y | - | - | - | - | - |
| `./data/lock/pgadvisory/integrationtest` | 1 | 1 | 1 | - | - | - | - | - | - | - | - | - | - |
| `./data/lock/redislock` | 3 | 3 | 1 | Y | Y | Y | Y | Y | Y | Y | - | - | - |
| `./data/queue/redisqueue` | 5 | 3 | 1 | Y | Y | Y | Y | Y | Y | Y | Y | - | Y |
| `./data/queue/redisqueue/integrationtest` | 1 | 1 | 1 | - | - | - | - | - | - | - | - | - | - |
| `./data/queue/riverqueue` | 1 | 2 | 0 | Y | Y | Y | Y | Y | - | - | - | - | Y |
| `./data/queue/riverqueue/integrationtest` | 1 | 1 | 1 | - | - | - | - | - | - | - | - | - | - |
| `./data/ratelimit/redis` | 1 | 1 | 0 | Y | Y | Y | Y | Y | Y | - | - | - | - |
| `./data/stream/redisstream` | 6 | 6 | 1 | Y | Y | Y | Y | Y | - | Y | Y | - | Y |
| `./data/stream/redisstream/integrationtest` | 1 | 1 | 1 | - | - | - | - | - | - | - | - | - | - |
| `./examples/agentic-service` | 2 | 1 | 1 | Y | - | Y | Y | Y | Y | Y | - | Y | Y |
| `./flags` | 2 | 1 | 0 | Y | Y | Y | Y | Y | - | - | - | - | Y |
| `./grpcx` | 12 | 15 | 2 | Y | Y | Y | Y | Y | - | Y | Y | Y | Y |
| `./httpx` | 91 | 64 | 21 | Y | Y | Y | Y | Y | Y | Y | Y | Y | Y |
| `./httpx/middleware/signedrequest/redis` | 2 | 1 | 1 | Y | Y | Y | Y | Y | - | - | - | Y | - |
| `./infra` | 71 | 45 | 11 | Y | Y | Y | Y | Y | Y | Y | Y | Y | Y |
| `./infra/leaderelection/pgadvisory` | 2 | 1 | 0 | Y | Y | Y | Y | Y | Y | Y | Y | - | - |
| `./infra/leaderelection/redislock` | 2 | 1 | 0 | Y | Y | Y | Y | Y | Y | Y | Y | - | - |
| `./infra/messaging/amqpbackend` | 14 | 12 | 1 | Y | Y | Y | Y | Y | Y | Y | Y | Y | Y |
| `./infra/messaging/amqpbackend/debughttp` | 3 | 1 | 1 | Y | - | Y | - | - | - | Y | - | Y | Y |
| `./infra/messaging/amqpbackend/integrationtest` | 3 | 6 | 2 | Y | - | Y | Y | Y | Y | - | - | - | - |
| `./infra/messaging/natsbackend` | 3 | 3 | 0 | Y | Y | Y | Y | Y | Y | Y | Y | Y | Y |
| `./infra/messaging/natsbackend/integrationtest` | 1 | 1 | 1 | - | - | - | - | - | - | - | - | - | - |
| `./infra/messaging/redisbackend` | 5 | 3 | 1 | Y | Y | Y | Y | Y | - | Y | - | - | Y |
| `./infra/redis` | 10 | 8 | 1 | Y | Y | Y | Y | Y | - | Y | Y | Y | - |
| `./infra/redis/redistest` | 2 | 1 | 1 | Y | - | - | Y | Y | Y | - | - | - | - |
| `./infra/sqldb/dbtest` | 2 | 0 | 1 | Y | - | - | - | Y | Y | - | - | Y | - |
| `./infra/sqldb/pgx` | 2 | 2 | 0 | Y | Y | Y | Y | Y | - | Y | Y | Y | Y |
| `./infra/sqldb/pgx/integrationtest` | 1 | 1 | 1 | - | - | - | - | - | - | - | - | - | - |
| `./infra/storage/azurebackend` | 4 | 4 | 1 | Y | Y | Y | Y | Y | Y | Y | Y | - | Y |
| `./infra/storage/gcsbackend` | 4 | 4 | 1 | Y | Y | Y | Y | Y | Y | Y | Y | Y | - |
| `./infra/storage/s3backend` | 9 | 4 | 1 | Y | Y | Y | Y | Y | Y | Y | Y | Y | Y |
| `./infra/storage/sftpbackend` | 6 | 4 | 1 | Y | Y | Y | Y | Y | Y | Y | Y | Y | - |
| `./infra/storage/storagehttp/uploadsec/clamav` | 3 | 1 | 1 | Y | Y | Y | Y | Y | - | - | Y | - | Y |
| `./infra/storage/storagetest` | 5 | 1 | 1 | Y | Y | - | Y | Y | - | - | - | Y | - |
| `./io` | 7 | 3 | 2 | Y | Y | Y | Y | Y | - | - | - | - | Y |
| `./observability` | 30 | 14 | 8 | Y | Y | Y | Y | Y | Y | Y | Y | Y | Y |
| `./observability/auditlog/postgres` | 3 | 1 | 1 | Y | Y | Y | Y | Y | - | Y | - | - | Y |
| `./observability/auditlog/postgres/integrationtest` | 1 | 1 | 1 | - | - | - | - | - | - | - | - | - | - |
| `./resilience` | 4 | 4 | 2 | Y | Y | Y | Y | Y | - | Y | Y | - | - |
| `./runtime` | 14 | 10 | 4 | Y | Y | Y | Y | Y | Y | Y | Y | - | Y |
| `./runtime/temporal` | 1 | 3 | 0 | Y | Y | Y | Y | Y | Y | Y | - | Y | - |
| `./security` | 14 | 12 | 2 | Y | Y | Y | Y | Y | Y | Y | Y | Y | Y |

## Per-Module Notes

| Module | Notes |
|---|---|
| `./app` | Builder and module lifecycle reviewed. Findings F-004/F-005 apply to fatal error logging, module-name logging, and internal address logging. TLS reload wiring was coherent. |
| `./app/amqp` | Builder adapter reviewed for TLS/credential propagation into AMQP backend. No new blocker beyond app-level logging/docs issues. |
| `./app/grpc` | gRPC app module reviewed. F-005 applies to raw listener address logging. |
| `./app/nats` | NATS app adapter reviewed for TLS source wiring and NATS backend credential options. No new blocker found. |
| `./app/postgres` | Postgres app adapter reviewed for password provider and reset behavior. No new blocker found. |
| `./app/redis` | Redis app adapter reviewed for plaintext/password guard and credential-provider acceptance. No new blocker found. |
| `./app/tracing` | Tracing app module reviewed for lifecycle/error logging shape. No new blocker beyond release-note wording checked under F-005. |
| `./authz` | Findings F-002/F-003 apply: audit sink contract mismatch and raw authz decision log fields. |
| `./authz/openfga` | API URL validation, safe HTTP client defaults, redirect blocking, TLS floor, and redaction tests reviewed. No new blocker found. |
| `./cmd/kit-bench-gate` | Public command surface and benchmark gating role reviewed. No new blocker found. |
| `./cmd/kit-doctor` | Security/audit command surface reviewed in the release inventory. No new blocker found in this pass. |
| `./cmd/kit-migrate` | Finding F-001 applies: new auditlog Postgres migrations are not in the registry. |
| `./cmd/kit-new` | Scaffold command surface reviewed for release inventory/doc consistency. No new blocker found in this pass. |
| `./cmd/kit-verify` | Verification command surface reviewed for release inventory. No new blocker found in this pass. |
| `./core` | Core config/error/redaction/validate/tenant/secret surfaces reviewed. Finding F-008 applies to `validate.Func`; F-004 depends on `redact.ErrorValue` behavior. |
| `./crypto` | Signing, PASETO, passhash, envelope, secret/masking adjacent surfaces reviewed from prior fixes and spot checks. No new blocker found. |
| `./crypto/encrypt/integrationtest` | Integration helper module reviewed as test-only surface. No new blocker found. |
| `./crypto/envelope/awskms` | AWS KMS key ID constraint checked. No new blocker found. |
| `./crypto/envelope/azurekeyvault` | Azure Key Vault key ID/version constraint checked. No new blocker found. |
| `./crypto/envelope/gcpkms` | GCP KMS key ID constraint checked. No new blocker found. |
| `./crypto/envelope/vaulttransit` | Vault Transit key constraint checked. No new blocker found. |
| `./data` | In-memory stores, cache, budget, stream, queue, idempotency, approval/actionlog, and tenant wrappers included in symbol scans. No new blocker found in this pass. |
| `./data/actionlog/postgres` | Existing migration-backed store reviewed as comparison for kit-migrate coverage. No new blocker found. |
| `./data/actionlog/postgres/integrationtest` | Integration helper module reviewed as test-only surface. No new blocker found. |
| `./data/approval/postgres` | Existing migration-backed store reviewed as comparison for kit-migrate coverage. No new blocker found. |
| `./data/approval/postgres/integrationtest` | Integration helper module reviewed as test-only surface. No new blocker found. |
| `./data/budget/redis` | Redis budget constructor bounds and TTL constraints included in scans. No new blocker found. |
| `./data/cache/rediscache` | Redis cache limits and metric option naming reviewed. C-001 applies to metric option naming. |
| `./data/cache/rediscache/integrationtest` | Integration helper module reviewed as test-only surface. No new blocker found. |
| `./data/idempotency/pgstore` | Existing migration-backed store reviewed as comparison for kit-migrate coverage. No new blocker found. |
| `./data/idempotency/pgstore/integrationtest` | Integration helper module reviewed as test-only surface. No new blocker found. |
| `./data/idempotency/redisstore` | Redis idempotency, owner token/fingerprint, and credential-provider-related paths included in scans. No new blocker found. |
| `./data/lock/pgadvisory` | `Extend` ping behavior reviewed as fixed. No new blocker found. |
| `./data/lock/pgadvisory/integrationtest` | Integration helper module reviewed as test-only surface. No new blocker found. |
| `./data/lock/redislock` | Lock/release/leadership health and callback-drain fixes included in scans. No new blocker found. |
| `./data/queue/redisqueue` | Heartbeat cancellation and size/limit signals reviewed. No new blocker found. |
| `./data/queue/redisqueue/integrationtest` | Integration helper module reviewed as test-only surface. No new blocker found. |
| `./data/queue/riverqueue` | Queue adapter API/limit signals reviewed. No new blocker found in this pass. |
| `./data/queue/riverqueue/integrationtest` | Integration helper module reviewed as test-only surface. No new blocker found. |
| `./data/ratelimit/redis` | Redis rate-limit constructor/default signals reviewed. No new blocker found. |
| `./data/stream/redisstream` | Header validation and stream metrics reviewed. No new blocker found. |
| `./data/stream/redisstream/integrationtest` | Integration helper module reviewed as test-only surface. No new blocker found. |
| `./examples/agentic-service` | Golden-path example included for docs/API consistency. No new blocker found. |
| `./flags` | Feature-flag API and default fallback semantics reviewed. No new blocker found. |
| `./grpcx` | Server/interceptor surfaces reviewed. F-005 applies to raw gRPC impersonation identity logs. |
| `./httpx` | Middleware/auth/request/logging/metrics surfaces included in scans. HTTP auth redaction is the positive comparison for F-003/F-005. |
| `./httpx/middleware/signedrequest/redis` | Redis nonce store context/credential signal reviewed. No new blocker found. |
| `./infra` | Messaging, storage, Redis, SQL, outbox, leader-election contracts included in cross-module scans. F-006 applies through storage adapters. |
| `./infra/leaderelection/pgadvisory` | Elector callback-drain/lifecycle metrics reviewed. C-001 applies to metric option naming. |
| `./infra/leaderelection/redislock` | Elector callback-drain/lifecycle metrics reviewed. C-001 applies to metric option naming. |
| `./infra/messaging/amqpbackend` | AMQP URL provider, TLS wiring, drain/shutdown, message size limits, and metrics reviewed. No new blocker found. |
| `./infra/messaging/amqpbackend/debughttp` | Debug HTTP helper boundaries and limits reviewed. No new blocker found. |
| `./infra/messaging/amqpbackend/integrationtest` | Integration helper module reviewed as test-only surface. No new blocker found. |
| `./infra/messaging/natsbackend` | NATS credential provider, TLS, drain/shutdown, size limits, and metrics reviewed. No new blocker found. |
| `./infra/messaging/natsbackend/integrationtest` | Integration helper module reviewed as test-only surface. No new blocker found. |
| `./infra/messaging/redisbackend` | Redis stream bridge and message size/limit behavior reviewed. No new blocker found. |
| `./infra/redis` | Redis connection safety, metrics, config, and credential-provider compatibility reviewed. No new blocker found. |
| `./infra/redis/redistest` | Integration helper module reviewed as test-only surface. No new blocker found. |
| `./infra/sqldb/dbtest` | Integration helper module reviewed as test-only surface. No new blocker found. |
| `./infra/sqldb/pgx` | Postgres password provider and pool reset behavior reviewed. No new blocker found. |
| `./infra/sqldb/pgx/integrationtest` | Integration helper module reviewed as test-only surface. No new blocker found. |
| `./infra/storage/azurebackend` | Finding F-006 applies to `Get` not-found metrics. |
| `./infra/storage/gcsbackend` | Finding F-006 applies to `Get` not-found metrics. |
| `./infra/storage/s3backend` | S3 is the positive comparison for F-006: `Get` normalizes not-found before metrics. |
| `./infra/storage/sftpbackend` | Finding F-006 applies to `Get` not-found metrics; password provider/host-key paths otherwise reviewed. |
| `./infra/storage/storagehttp/uploadsec/clamav` | Scanner fail-closed/temp cleanup and prior race fix reviewed. No new blocker found. |
| `./infra/storage/storagetest` | Storage compliance helper reviewed as test/support surface. No new blocker found. |
| `./io` | Atomic file and progress helpers included in scans. No new blocker found. |
| `./observability` | Findings F-002/F-007 and caveat C-002 apply through auditlog and docs/dashboard contracts. |
| `./observability/auditlog/postgres` | Findings F-001/F-007 apply; C-002 applies if Store is public user API. |
| `./observability/auditlog/postgres/integrationtest` | Integration helper module reviewed as test-only surface. F-007 applies to release docs inventory. |
| `./resilience` | Retry/circuit-breaker error/context semantics included in scans. No new blocker found. |
| `./runtime` | Lifecycle, eventbus, cron, batchworker, and fanout surfaces included in scans. No new blocker found. |
| `./runtime/temporal` | Adapter dependency isolation and redacted dial failure behavior reviewed. No new blocker found. |
| `./security` | TLS reload, mTLS identity, SSRF/JWT-related surfaces included in scans. No new blocker found in current changed files. |

## Audited Clean Or Already Fixed In Current Source

These were explicitly checked because they were previously high-risk or externally reported:

- AWS/GCP/Azure/Vault envelope KMS unwrap paths constrain caller-supplied key IDs before decrypt/unwrap.
- `data/lock/pgadvisory.sessionLock.Extend` performs I/O via ping instead of returning a blind success.
- Redis construction through app-level wiring enforces plaintext/passwordless safety for non-local addresses.
- `crypto/signing` has standardized signing/verification argument order in current source.
- `data/cache/compute` foreground singleflight observes close/drain semantics and followers watch cache close.
- `resilience/retry` preserves function errors when context cancellation races with return.
- `data/budget/memory` sweep/consume orphan-bucket race was addressed in current source.
- `httpx/middleware/signedrequest` resolves the secret before full body buffering and streams the body hash.
- PASETO custom-claim reserved-name rejection exists on verify as well as build.
- ClamAV remove-on-EOF cleanup uses single-execution semantics.
- Redis stream message header validation returns an error instead of panicking.
- Eventbus shutdown recover path reports stopped state instead of success.
- Redis queue heartbeat permanent failure cancels local processing.
- `core/tenant.WithID` now returns an error for invalid/cross-tenant rebind cases.
- `storage.Manager.Backend(name)` returns `(Storage, error)` and `MustBackend` is the panic variant.
- `apperror.ConflictError` default retryability is false.
- Trusted S2S test helpers are build-tagged behind `authtest`.
- HTTP/gRPC permission checks fail closed for missing permissions unless the explicit trusted-S2S bypass is active.

## Not Reviewed / Residual Risk

No `go.work` module was skipped.

Residual risk:

- Docker-backed integration tests were inspected but not executed in this review pass.
- The review used source/test/doc scans and targeted manual reading; it is still not a formal proof of every branch in every implementation.
- Large dashboards and generated release artifacts were checked for the relevant contracts above, not line-by-line for prose quality.
