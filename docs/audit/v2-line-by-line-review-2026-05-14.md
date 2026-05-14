# rho-kit v2.0.0 line-by-line review

Date: 2026-05-14
Commit reviewed: `8f81252`
Workspace: 77 Go modules

This review was run after the earlier hostile-review rounds because the
previous passes left explicit gaps: docs/runbooks were only sampled, many
modules were inspected by axis rather than literally line by line, examples
were not compiled comprehensively, and integration-test modules were not all
compile-checked.

## Scope actually covered

- All tracked Go package source and tests across the 77 workspace modules.
- All command modules under `cmd/`.
- All tracked root policy and release files: `AGENTS.md`, `README.md`,
  `CHANGELOG.md`, `LICENSE.md`, `NOTICE`, `SECURITY.md`, `.cursorrules`,
  `.gitignore`, `Makefile`, `.github/*`, `go.work`, and `go.work.sum`.
- All tracked docs under `docs/ai`, `docs/audit`, `docs/release`, and
  `docs/RELEASE_NOTES_v2.md`.
- All tracked shell tooling under `tools/`.
- All dashboard JSON and Prometheus rule YAML under `observability/dashboards`.
- All tracked SQL migrations.
- Benchmark baseline files under `docs/release/benchmarks/v2.0.0`.

Generated checksum files (`go.sum` files and `go.work.sum`) were treated as
dependency inventories and checked through the dependency, publishability, and
license gates; their individual hash lines were not assigned semantic review
findings.

## Live verification run

Passing:

- `bash -n tools/*.sh`
- `make check-operational-readiness` -> `77 modules covered`
- `make check-api-freeze-coverage` -> `77 modules covered`
- `make check-dependency-allowlist` -> `59 direct external deps approved`
- `make check-dependency-boundaries` -> `413 direct module edges checked`
- `make check-dashboard-metrics` -> `113 candidates; 13 allowlisted`
- `make check-dashboards` -> Grafana JSON valid; Prometheus rules valid
- `make check-no-binaries` -> `1229 files scanned`
- `RELEASE_MODE=all make release-plan` -> `77 modules`, levels `0..5`
- `make check-release-team` -> `@bds421/security` exists and CODEOWNERS is
  enforced on `main`
- `make check-publishable`
- `make check-licenses`
- Unit/package tests that were run during the pass: `app`, all split `app/*`
  modules, `core`, `runtime`, `runtime/temporal`, `resilience`, `io`,
  `observability`, `observability/auditlog/postgres`, `security`,
  `infra/messaging` base and backends, command modules, and
  `examples/agentic-service`.

Failing:

- Compile-only integration sweep:
  `for mod in $(git ls-files '*integrationtest/go.mod' | xargs -n1 dirname | sort); do (cd "$mod" && go test -tags integration -run TestNonExistent ./...); done`
  failed for:
  - `infra/messaging/amqpbackend/integrationtest`: tests still call
    `conn.Close`, but `*amqpbackend.Connection` no longer has that method.
  - `infra/messaging/natsbackend/integrationtest`: tests still call
    `conn.Close`, but `*natsbackend.Connection` no longer has that method.

Not run in this pass:

- Full Docker-backed integration execution, because compile-only already found
  release-blocking stale integration modules.
- Full `make test-race`, long fuzzing campaigns, and full benchmark
  comparisons.
- Live cloud-provider checks for AWS/GCP/Azure/Vault KMS or object storage.
- Full release rehearsal/tag dry run.

## Release-blocking findings

### B-001: app HTTP client tracing cannot activate through normal Builder order

Evidence:

- `app/builder_helpers.go:116-122` prepends the built-in HTTP client module.
- `app/builder.go:1032-1037` appends user modules after built-ins.
- `app/module.go:229-245` only adds modules to `ModuleContext.modules` after
  they have initialized.
- `app/httpclient_module.go:58-75` probes `TracingProvider.TracingActive()`
  while the tracing module is still uninitialized and absent from the module
  map.
- `app/tracing/tracing.go:45-68` sets `active=true` only during tracing
  module initialization.

Impact: `app/tracing` documents that the default HTTP client gets tracing
instrumentation, but a real Builder setup initializes the HTTP client before
tracing can report active. Outbound HTTP traces are silently missing.

Fix direction: either initialize tracing before the HTTP client when tracing is
registered, or make the HTTP client wrapping decision independent of the
runtime `TracingActive()` state.

### B-002: AMQP and NATS integration modules do not compile

Evidence:

- `infra/messaging/amqpbackend/integrationtest` build fails with repeated
  `conn.Close undefined (type *amqpbackend.Connection has no field or method Close)`.
- `infra/messaging/natsbackend/integrationtest` build fails with
  `conn.Close undefined (type *natsbackend.Connection has no field or method Close)`.

Impact: the release tree advertises Docker-backed integration tests, but two
messaging integration modules are stale against the current public API.

Fix direction: update the integration tests to use the current close/shutdown
API, then run the real Docker-backed integration tests.

### B-003: Postgres outbox FetchPending SQL is not valid PostgreSQL

Evidence:

- `infra/outbox/postgres/store.go:124-148` uses `row_number() OVER (...)` in a
  CTE that also uses `FOR UPDATE SKIP LOCKED`.

Impact: PostgreSQL rejects `FOR UPDATE` with window functions. The relay claim
path can fail at runtime instead of dispatching outbox rows.

Fix direction: split the claim query so locking happens in a subquery/CTE that
does not use window functions, then preserve ordering in a later projection.

### B-004: `core/secret.String.Equal` can return true for different lengths

Evidence:

- `core/secret/secret.go:221-241` folds length mismatch into
  `byte(len(a) ^ len(b))`.

Impact: length differences that are multiples of 256 collapse to zero. If the
extra bytes in the longer secret are zero, the equality helper can report
equality for unequal byte strings.

Fix direction: keep constant-time byte comparison, but fold length equality
with an integer accumulator that cannot truncate to one byte.

### B-005: license gate can pass with incomplete scan output

Evidence:

- `tools/check-licenses.sh:83-86` runs `go-licenses ... || true` for each
  module and appends whatever CSV was produced.
- The script only validates the accumulated CSV rows afterward.

Impact: a per-module scanner failure can silently drop dependency rows and
still pass if other modules produce allowlisted rows. The live gate currently
passes, but the gate is weaker than the release policy claims.

Fix direction: capture scanner exit status per module, print the module's
stderr on failure, and fail the license gate if any module scan failed.

## High-priority correctness and API findings

- `crypto/passhash/passhash.go:60-80`: the default verification limit still
  permits 1 GiB (`MaxMemory: 1 * 1024 * 1024` KiB), despite comments implying
  the default cannot allocate gigabytes.
- `crypto/envelope/kekstatic`: unwrap reads a key slice under lock and then
  uses it after unlock while `RemoveKey`/`Close` can zero the same slice.
- `crypto/encrypt/encrypt.go`: `FieldEncryptor.RegisterMetrics` writes the
  operation hook while normal encryption/decryption reads it; the type is used
  as concurrent-safe elsewhere.
- `crypto/paseto`: `V4Local.Close` zeroes exported bytes, not necessarily the
  provider's live key material.
- `crypto/envelope/awskms/metrics.go`: AWS KMS metrics use the process-global
  Prometheus registerer and do not follow the package-wide `WithRegisterer`
  contract.
- `crypto/envelope/gcpkms`: version `0` is allowed despite docs saying the
  version must be positive.
- `crypto/envelope/azurekeyvault`: vault host comparison is case-sensitive.
- `data/idempotency/redisstore`: `Set` and `Unlock` read stored values without
  the same pre-read size check used by `Get`/`TryLock`.
- `data/idempotency/pgstore`: stored response rows are scanned before payload
  size validation.
- `data/cache/rediscache`: `MGet` can read oversized Redis values before
  applying the package's max-value contract.
- `data/lock/redislock`: `Release` clears the local token on ambiguous Redis
  backend errors.
- `data/queue/riverqueue`: docs/API imply message ID de-duplication that the
  wrapper cannot guarantee by itself.
- `data/stream/redisstream`: producer header maps are unbounded; batch publish
  metrics can undercount partial successes after a pipeline error.
- `authz/openfga`: a fully custom HTTP client can bypass the kit's TLS/client
  hardening.
- `httpx/pagination`: cursor decoding and result building still have direct
  length/negative-limit edge cases.
- `httpx/middleware/timeout`: late panics after a hard timeout are not surfaced
  to the caller path.
- `httpx/middleware/stack`: default metrics still bind to the default
  Prometheus registerer; no stack-level registerer option was found.
- `httpx/mcp`: async audit stop cannot be retried after a timeout.
- `grpcx`: raw gRPC options can undo hardened defaults, and `GRPCStatus` lacks
  an explicit `CodeStorageFull` mapping.
- `infra/redis`: `Fields.ValidateRedis` enforces FR-077, but callers using
  `Fields.Redis.Options()` plus `Connect` can bypass the plaintext/passwordless
  guard.
- `infra/sqldb`: `Fields.Validate` allows `sslmode=require`, while the pgx
  connection wrapper rejects it by default; the preflight and dial contracts do
  not match.
- `infra/storage`: `Manager.Default` can return a backend after `Manager.Close`;
  `Migrate` can return nil even when per-object migrations failed.
- `infra/storage`: retry/circuitbreaker decorators hide optional backend
  capabilities and do not consistently retry/measure lazy list iteration.
- `infra/storage/storagehttp/uploadsec`: `AllowSVG` discards sanitizer output.
- `infra/storage/sftpbackend`: static password/key material has no zeroization
  lifecycle; several operations ignore cancellation after connection setup.
- `infra/messaging`: `BufferedPublisher.load` silently drops invalid persisted
  state entries by default.
- `infra/messaging/amqpbackend`: `Config.Validate` accepts some URL/field
  combinations that `Connect` later rejects; reconnect hooks are cooperative
  only.
- `infra/messaging/natsbackend`: `ExtraOptions` can override hardened defaults;
  credential-provider timeouts are cooperative only.
- `observability/slo`: config validation is thin and gather errors are
  discarded.
- `observability/tracing`: fallback/noop behavior can ignore `EnableBaggage`
  and overstates collector reachability guarantees.
- `runtime/lifecycle`: stop timeout docs overstate total shutdown bounding when
  salvage budgets are active.
- `runtime/eventbus`: `Publish(nil)` can panic on saturated `OnFullBlock`.
- `runtime/temporal`: `Worker.Stop(ctx)` ignores `ctx`; `Connect` hides the
  dial cause.
- `security/jwtutil`: a fully custom JWKS HTTP client can bypass the TLS floor.
- `security/jwtutil/revocation`: audit hooks can panic after a successful
  mutation.
- `security/asvs`: package registry is stale/incomplete for the current
  security/crypto/storage surface.

## Documentation and release-artifact blockers

- `README.md:58-61` and root golden-path docs pass a `rediss://...` URL as
  `go-redis Options.Addr`; go-redis expects `host:port` unless the URL parser
  path is used.
- `README.md:107` and `docs/ai/redis.md` use `rediscache.NewRedisCache`, but
  the current constructor is `rediscache.NewCache`.
- `AGENTS.md:159` points users to nonexistent `redis/queue.DepthCheck`.
- `AGENTS.md:237-239` still talks about Builder-created RabbitMQ/NATS message
  size methods that no longer exist on the root Builder.
- `docs/ai/adoption.md`, `docs/ai/sqldb.md`, `docs/ai/messaging.md`,
  `docs/ai/observability.md`, `docs/ai/credential-rotation.md`, and
  `docs/ai/redis.md` still contain removed Builder APIs such as
  `WithPostgres`, `WithRedis`, `WithRabbitMQ`, `WithNATS`, `WithTracing`,
  `WithMaxMessageBytes`, `WithRouteMaxMessageBytes`, and
  `app.WithRabbitMQURLProvider`.
- `docs/ai/bootstrap.md`, `docs/ai/adoption.md`, `docs/ai/http.md`, and
  `docs/RELEASE_NOTES_v2.md` still show the old
  `WithMultiTenant(extractor, true)` signature.
- `docs/ai/http.md:197` recommends wrapping `http.DefaultTransport`, directly
  conflicting with the repo anti-pattern.
- `docs/ai/utilities.md` omits `CodeStorageFull` and has a stale
  `httpx.WriteJSON(w, 200, result)` signature.
- `docs/audit/SUPPLY_CHAIN.md:40-45` requires every module to have a pinned
  `toolchain` directive, but modules do not have that directive and the
  publishability gate does not enforce it.
- `docs/audit/SUPPLY_CHAIN.md:642-678` still describes the kit as proprietary,
  while `LICENSE.md`, `NOTICE`, `README.md`, and root `AGENTS.md` now state
  Apache-2.0.
- `docs/audit/README.md` says completed audit reports are not kept, but the
  directory still contains dated stale audit artifacts with old module counts,
  dirty-tree status, and resolved findings.
- `docs/release/API_FREEZE_V2.md:42-44` freezes removed `Builder.WithNATS`,
  `Builder.WithPostgres`, and `Builder.WithRedis` names.
- `docs/release/MIGRATION_V2.md:87-97` contains stale pre-v2 golden-path code
  in the current migration guide, even though the later mapping table is closer
  to the actual adapter-module design.
- `docs/RELEASE_NOTES_v2.md:9-25` says lazy adapters already landed, while
  `docs/RELEASE_NOTES_v2.md:58-67` says the lazy-adapter split is planned for
  v2.1. The first statement matches the current tree; the second is stale.
- `docs/RELEASE_NOTES_v2.md:77-80`, `1090-1122`, and `1246` still document
  removed Builder methods.
- `docs/RELEASE_NOTES_v2.md:1295-1305` says "No code changes are required"
  for v1.x upgrades, contradicting the v2 import-path/API migration sections.
- `cmd/kit-verify/go.mod` claims HSTS proof, but `cmd/kit-verify/main.go`
  does not probe HSTS.
- `cmd/kit-migrate/CHANGES.md` and many package `CHANGES.md` files are stale
  v1-era documents and mention removed GORM modules.

## Prometheus contract caveat

The live dashboard checks pass syntactically, but the current gate is not a
full Prometheus contract freeze:

- `tools/check-dashboard-metrics.sh` matches metric names in Go source and an
  explicit allowlist; it does not instantiate collectors, inspect descriptors,
  validate label sets, or prove every dashboard query works with a custom
  registerer.
- Several docs and dashboards assume namespace/service labels that individual
  collectors do not necessarily emit unless a service wraps/relabels metrics.
- AMQP/NATS metrics constructors and docs have drifted in places from the
  `WithRegisterer` convention.

Before calling Prometheus stable, add descriptor-level tests for every public
collector and dashboard query families, including names, labels, help text, and
registerer behavior.

## Findings from previous reviews that are now closed

- AWS/GCP KMS unwrap now constrains the caller-supplied key ID before SDK
  decrypt.
- Azure/GCS/S3 not-found storage metrics now normalize expected not-found
  results before recording operation errors.
- `data/lock/pgadvisory.Extend` now performs I/O instead of being a no-op.
- HTTP S2S auth no longer defaults to allow on empty permissions.
- `httpx/middleware/signedrequest` resolves the secret before reading the full
  request body and now spills large bodies instead of buffering the full body in
  memory.
- `data/cache/compute` foreground singleflight close behavior was fixed.
- `resilience/retry` no longer blindly swallows function errors on simultaneous
  context cancellation.
- `data/budget/memory.Consume` now verifies the bucket is still the live map
  entry after locking (`memory.go:247-259`), so the earlier double-grant
  sweep race is not open for `Consume`.
- `uploadsec/clamav` no longer has the earlier unsynchronized `removed` bool.
- `runtime/eventbus` closed-pool publish now reports stopped rather than
  success.
- `redisqueue` heartbeat permanent-failure now cancels processing instead of
  leaving local work running.

## Module coverage matrix

Legend: `LR` means line-read in this pass. `UT` means package/unit tests were
run in this pass. `IC` means compile-only integration test was run.

| Module | Coverage | Open finding summary |
|---|---:|---|
| app | LR, UT | B-001; app-level Prometheus registerer gaps; split adapter docs drift. |
| app/amqp | LR, UT | Cooperative reconnect/provider timeout caveats; docs around Builder message-size methods stale. |
| app/grpc | LR, UT | Listen error loses cause; shutdown docs need sharper operator signal. |
| app/nats | LR, UT | Registerer propagation gap; docs expose removed `WithNATS`. |
| app/postgres | LR, UT | `Stop(ctx)` ignores ctx while closing pgxpool. |
| app/redis | LR, UT | FR-077 fixed in module path; docs still show URL in `Options.Addr`. |
| app/tracing | LR, UT | Tracing active state cannot affect built-in HTTP client because of init order. |
| authz | LR | Logging/audit edge cases; typed nil audit sink caveat. |
| authz/openfga | LR | Custom HTTP client can bypass kit TLS defaults. |
| cmd/kit-bench-gate | LR, UT | `-count=N` benchmark samples collapse to the last sample. |
| cmd/kit-doctor | LR, UT | No release blocker found; rule coverage remains pattern-based. |
| cmd/kit-migrate | LR, UT | Stale `CHANGES.md`; migration surface may omit newer stores. |
| cmd/kit-new | LR, UT | Template comments overclaim Builder JWT/TLS wiring. |
| cmd/kit-verify | LR, UT | HSTS proof is documented but not implemented. |
| core | LR, UT | B-004; stale apperror docs; context ID validation gaps. |
| crypto | LR | passhash max-memory default; FieldEncryptor metric hook race; PASETO close limitations. |
| crypto/encrypt/integrationtest | LR, IC | Compile-only OK. |
| crypto/envelope/awskms | LR | Metrics registerer convention gap. |
| crypto/envelope/azurekeyvault | LR | Host comparison case sensitivity. |
| crypto/envelope/gcpkms | LR | Version `0` accepted despite positive-version docs. |
| crypto/envelope/vaulttransit | LR | No high-confidence blocker found. |
| data | LR | Actionlog/approval/idempotency/budget/cache shared findings. |
| data/actionlog/postgres | LR | Migration comments still say prev_hash is HMAC while code uses plain chain hash. |
| data/actionlog/postgres/integrationtest | LR, IC | Compile-only OK. |
| data/approval/postgres | LR | Payload scan before cap; tenant-store TOCTOU caveat. |
| data/approval/postgres/integrationtest | LR, IC | Compile-only OK. |
| data/budget/redis | LR | Numeric precision/config limits for sub-micro rates; memory double-grant finding closed. |
| data/cache/rediscache | LR | MGet oversize-before-allocation gap. |
| data/cache/rediscache/integrationtest | LR, IC | Compile-only OK. |
| data/idempotency/pgstore | LR | Stored response scanned before size validation. |
| data/idempotency/pgstore/integrationtest | LR, IC | Compile-only OK. |
| data/idempotency/redisstore | LR | Set/Unlock missing pre-read size check. |
| data/lock/pgadvisory | LR | Earlier Extend bug fixed. |
| data/lock/pgadvisory/integrationtest | LR, IC | Compile-only OK. |
| data/lock/redislock | LR | Release clears local token on ambiguous backend error. |
| data/queue/redisqueue | LR | Panic on UUID generation error discards cause; old heartbeat bug fixed. |
| data/queue/redisqueue/integrationtest | LR, IC | Compile-only OK. |
| data/queue/riverqueue | LR | ID de-duplication promise is too strong. |
| data/queue/riverqueue/integrationtest | LR, IC | Compile-only OK. |
| data/ratelimit/redis | LR | Sub-micro rate precision/config edge. |
| data/stream/redisstream | LR | Header bounds and partial-success metric caveats. |
| data/stream/redisstream/integrationtest | LR, IC | Compile-only OK. |
| examples/agentic-service | LR, UT | No blocker; example is explicitly non-production. |
| flags | LR | No high-confidence blocker; validation surface is intentionally small. |
| grpcx | LR | `CodeStorageFull` mapping and raw-option hardening caveats. |
| httpx | LR | pagination/sign/openapi/slohttp edge cases; docs drift. |
| httpx/middleware/signedrequest/redis | LR | Compile not separately needed; request ctx propagation still worth tightening. |
| infra | LR | Storage/messaging/sqldb/outbox shared findings. |
| infra/leaderelection/pgadvisory | LR | Callback drain timeout is cooperative; `Run(nil, ...)` start-state caveat. |
| infra/leaderelection/redislock | LR | Same callback-drain caveat. |
| infra/messaging/amqpbackend | LR, UT | Config/dial mismatch, cooperative reconnect caveats. |
| infra/messaging/amqpbackend/debughttp | LR, UT | No blocker found. |
| infra/messaging/amqpbackend/integrationtest | LR, IC fail | B-002. |
| infra/messaging/natsbackend | LR, UT | ExtraOptions can undo hardening; provider timeout cooperative. |
| infra/messaging/natsbackend/integrationtest | LR, IC fail | B-002. |
| infra/messaging/redisbackend | LR, UT | Docs overstate retry binding. |
| infra/outbox/postgres | LR | B-003; unbounded JSON/BYTEA scan before validation. |
| infra/outbox/postgres/integrationtest | LR, IC | Compile-only OK; real SQL execution still must be rerun after B-003 fix. |
| infra/redis | LR | FR-077 bypass through `Options()` + direct `Connect`. |
| infra/redis/redistest | LR | Test helper only; no production blocker. |
| infra/sqldb/dbtest | LR | Test helper only. |
| infra/sqldb/pgx | LR | sslmode preflight mismatch and raw scan caveat. |
| infra/sqldb/pgx/integrationtest | LR, IC | Compile-only OK. |
| infra/storage/azurebackend | LR | Case sensitivity and docs/credential-mode gaps. |
| infra/storage/gcsbackend | LR | Optional listing/docs matrix gap. |
| infra/storage/s3backend | LR | nil-context health panic and env credential docs drift. |
| infra/storage/sftpbackend | LR | Static secret zeroization and cancellation gaps. |
| infra/storage/storagehttp/uploadsec/clamav | LR | Earlier race fixed; max+1 overflow caveat remains. |
| infra/storage/storagetest | LR | Missing arbitrary string-prefix list contract test. |
| io | LR, UT | atomicfile docs mention unimplemented `LoadBounded`; load guard racy. |
| observability | LR, UT | SLO/tracing/dashboards/registerer contract caveats. |
| observability/auditlog/postgres | LR, UT | Cursor input length cap and migration/docs drift. |
| observability/auditlog/postgres/integrationtest | LR, IC | Compile-only OK. |
| resilience | LR, UT | Circuitbreaker docs disagree with default cancellation classification. |
| runtime | LR, UT | Lifecycle timeout docs; eventbus nil publish edge; cron timeout overpromise. |
| runtime/temporal | LR, UT | Stop ignores ctx; worker start/stop cause handling weak. |
| security | LR, UT | ASVS registry stale; JWKS custom client can bypass TLS floor. |

