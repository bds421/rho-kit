# Changelog

## Unreleased

Security maintenance: Go toolchain and gRPC. Suggested version: **v2.8.0**,
because the minimum Go version every consumer must satisfy rises
(1.26.2 → 1.26.8); no API changes.

- **fix(infra/storage/localbackend): escape detection broken on Go ≥ 1.26.6.**
  Go 1.26.6 changed how `os.Root.MkdirAll` reports a path component that is
  itself a symlink escaping the root. The rejection is now nested
  (`PathError{mkdirat, PathError{statat, "path escapes from parent"}}`), and
  `isEscapeError` only matched the outermost `PathError`. The write was
  **still refused** (os.Root held, and no bytes landed outside the root), but
  the refusal was misreported as a generic `create dirs failed` instead of the
  redacted `unsafe parent` error. As a result, `TestLocalBackend_Put/rejects_symlinked_parent`
  and both `TestTOCTOU_EscapingSymlinkComponentRefused` write cases failed on
  every patched toolchain. CI stayed green only because it ran Go 1.26.5.
  `isEscapeError` now walks the whole wrap chain. The new
  `TestIsEscapeError_ErrorShapes` pins both error shapes independently of the
  toolchain running the tests.
- **security(go): minimum Go 1.26.2 → 1.26.8 across all 111 modules;
  workspace toolchain go1.27.1; CI `go-version` 1.26.5 → 1.27.1.** govulncheck
  reported reachable standard-library advisories in every gRPC-using module:
  GO-2026-5026, -5037, -5039, -5856, -5972, -6090 and -6218 (`net/http`,
  `crypto/tls`, `net/url`, `encoding/asn1`, ...), all fixed in Go ≤ 1.26.6. The
  workspace pinned `toolchain go1.26.2` and CI ran 1.26.5, so builds, tests
  and vulncheck all ran on affected toolchains. Modules declare 1.26.8 (the
  lowest release clearing every advisory) rather than 1.27, so consumers are
  not forced onto a new major toolchain.
- **security(grpc): google.golang.org/grpc v1.83.0 → v1.83.2** in `grpcx`,
  `observability`, `app/tracing`, `app/grpc`, `crypto/envelope/gcpkms`,
  `infra/secrets/gcpsm`, `infra/leaderelection/etcd` and
  `infra/storage/gcsbackend`. Fixes GO-2026-6443 (server panic via a missing
  `:authority`/Host header; reachable from `app/grpc`) and GO-2026-6348.
  v1.83.2 is deliberately chosen over v1.84.0: the 1.84 line has no fixed
  stable release for GO-2026-6443 yet. Move to the first stable ≥ v1.85.0.
- **security(x/crypto): golang.org/x/crypto → v0.57.0** in the 30 modules that
  required an older version. Fixes GO-2026-6354 and GO-2026-6355 (fixed in
  v0.56.0), reachable from `infra/storage/sftpbackend`.
- **chore(tooling): golangci-lint v2.10.1 → v2.14.0, govulncheck v1.1.4 →
  v1.8.0.** Both older releases bundle an `x/tools` that cannot read Go 1.27
  export data (`export data version 4 is greater than maximum supported
  version 2`), so every lint run failed with typecheck errors on the new
  toolchain. v2.14.0's staticcheck flagged one new QF1001 in
  `riverqueue.validListenerSchema`; rewritten with De Morgan's law, same
  semantics (pinned by the existing `Public`/`1public` rejection cases).
  It also flagged SA1019 on `pyroscope.Config.AuthToken` (deprecated upstream
  in favour of `BasicAuthUser`/`BasicAuthPassword`). It is suppressed with a
  reason, because `observability/pyroscope.Config.AuthToken` is this kit's
  documented bearer-token API. **Follow-up:** add basic-auth fields before
  pyroscope-go removes `AuthToken`.
- **chore(riverqueue): tidy + allowlist `github.com/riverqueue/river/riverdriver`.**
  `dfe5ae9b` (listener schema) imports `riverdriver` directly but left it
  `// indirect`, so `check-tidy` failed on `main`. Tidying makes it a direct
  dependency; it is added to `docs/audit/dependency-allowlist.txt` next to the
  already-approved `river`, `riverpgxv5` and `rivertype` (same upstream
  project). **Needs reviewer sign-off per the allowlist policy.**

## v2.7.2 — 2026-09-04

Patch release for the HTTP middleware stack (module tag `httpx/v2.7.1`; no
other module changes).

- **fix(httpx/middleware/metrics).** The route-pattern slot that `CaptureRoute`
  fills for the metrics and tracing middleware was a plain string shared across
  goroutines. Under `timeout.Timeout` the inner chain runs on its own goroutine
  and the outer chain returns the moment the request context is cancelled
  (client disconnect, deadline), so `finishHTTPSpan` and the metrics `defer`
  could read the slot while `CaptureRoute` was still writing it: a data race,
  observed as a torn string read in a consumer's `-race` suite
  (sigma-tkgd `TestReplication_EndToEnd_Converges`, a replica long-poll
  cancelled at shutdown). The slot now holds the pattern behind an
  `atomic.Pointer[string]` with `set`/`get` accessors; the three call sites are
  unchanged in behaviour. No API change. Regression probe
  `TestCaptureRoute_ConcurrentOuterReadIsRaceFree` races an outer read against
  the capture 200 times and fails under `-race` on the previous code.

## v2.7.1 — 2026-08-06

Patch release for role-isolated River workers.

- **fix(riverqueue).** Add `DriverFromPoolWithListenerSchema` so workers can
  retain role-local leadership tables while subscribing to the shared
  LISTEN/NOTIFY namespace used by producers. The listener schema is an exact,
  validated unquoted PostgreSQL identifier and does not change executor,
  migration, or leadership schema selection.

## v2.7.0 — 2026-08-03

Object-storage exact-generation release (coordination tag `release/v2.7.0`).
Additive only; no v2 API or behavior changes for existing callers.

- **feat(storage).** Add paged exact-generation enumeration
  (`ExactVersionPageLister`, `ExactVersionCursor`, `ExactVersionPage`) as the
  recovery and export complement to the bounded legal-erasure listers. Cursors
  are opaque and bind key plus generation; delete markers and tombstones count
  against the page limit, and backends fail closed when a provider reports
  truncation without an advancing cursor. Implemented for the memory, local,
  and S3 backends and forwarded through the retry, circuit-breaker, and
  encryption decorators.
- **feat(storage).** Add `versionrelocation`, an opt-in decorator that resolves
  historical exact-generation identities to their current physical generation
  through a caller-owned resolver. Unversioned operations and physical
  enumeration remain unchanged; resolvers may move a generation but never
  rewrite an object key.
- **fix(storage/s3).** Use `WhenRequired` response checksum validation for
  explicitly configured S3-compatible endpoints, which commonly omit optional
  response checksum headers and otherwise produced one SDK warning per
  successful `GetObject`. Native AWS keeps its SDK/operator-selected policy.
- **chore(deps).** Refresh every direct external dependency to its latest
  minor/patch release, including the AWS SDK, smithy-go, gRPC, Prometheus
  client, jwx, goose, River, Kubernetes client, and the Google Cloud SDKs.

## v2.6.0 — 2026-07-22

Platform-foundations release (coordination tag `release/v2.6.0`).

- **feat(identity).** Add a canonical authenticated principal and consistent
  HTTP/gRPC, authorization, and audit-actor projection.
- **feat(auth).** Add standards-based OIDC relying-party wiring, hardened
  OAuth2 browser-flow state storage, and client-credentials lifecycle support.
- **feat(contracts).** Add versioned contract artifacts, compatibility checks,
  CI validation, and generated-service contract scaffolding.
- **feat(messaging).** Add a durable PostgreSQL transactional inbox paired
  with outbox schema-version handling and Docker-backed recovery coverage.
- **feat(tooling).** Add production scaffolding, contract and OIDC doctor
  rules, catalog reporting, ADRs, and release verification guidance.

## v2.5.1 — 2026-07-22

Patch release for S3-compatible object storage.

- **fix(storage/s3).** Keep large `PutObject` requests compatible with custom
  endpoints that reject optional AWS chunked checksums, while preserving the
  SDK/operator request and response checksum policies for native AWS S3.

## v2.5.0 — 2026-07-21

Repository-wide review-remediation and hardening release (coordination tag
`release/v2.5.0`). This release contains intentional v2 API and behavior
changes; downstreams should review the affected package documentation before
upgrading.

- **security and transport.** Harden JWT issuer/audience policy, HTTP and gRPC
  service identity and entitlement propagation, secret handling, KMS adapters,
  webhook SSRF protection, websocket liveness, and fail-closed middleware.
- **data and messaging.** Make tenant idempotency keys unforgeable, require
  atomic tenant approval mutations, correct budget refund windows, reject
  unknown message schemas by default, and return lifecycle errors from queue
  and stream consumers.
- **runtime and infrastructure.** Harden claim leases, leader callback drains,
  SQL pools, Redis locks, secrets caches, storage bounds, journal safety, saga
  recovery, retry redaction, audit chains, and readiness caching.
- **API cleanup.** Unify the redislock option export surface, make package-level
  websocket handling track connections by default, and retire dead storage
  capability APIs.
- **release engineering.** Synchronize all 107 workspace modules after the
  upstream dependency bump, enforce tidy-command failures, standardize Go
  1.26.5, and make the Redis queue double-process guard deterministic.
- **documentation.** Close completed review/backlog trackers while retaining
  durable operational runbooks and dashboard guidance.

## v2.4.0 — 2026-07-18

Additive storage and portability-fix release (coordination tag
`release/v2.4.0`). No breaking changes.

- **feat(storage).** Add portable, checksummed S3 multipart uploads with
  bounded part spooling, idempotent complete/abort, stale-upload pagination,
  retry/circuit-breaker capability preservation, and Docker MinIO plus
  opt-in AWS conformance tests.
- **ci.** Run release, CI, dashboard, and supply-chain workflows on the patched
  Go 1.26.5 toolchain.
- **fix(approval/postgres).** Normalize decision timestamps to PostgreSQL's
  microsecond precision before returning them, so idempotent reads reproduce
  the original decision exactly.
- **fix(auditlog).** Normalize event timestamps before chain signing so the
  PostgreSQL adapter can verify HMAC chains after a timestamp round trip.

## v2.3.1 — 2026-06-28

Patch release (coordination tag `release/v2.3.1`).

- **fix(auth).** Remove `looksLikePrefixedMachineToken` session skip heuristic that
  falsely rejected one-dot session bearer tokens with `usr_` prefixes or
  base64url underscores. Machine credentials still fall through via wire-shape
  (no dot) and prefix-specific chain strategies.

## v2.3.0 — 2026-06-28

Additive feature release across the `/v2` module set (coordination tag
`release/v2.3.0`). No breaking changes.

- **feat(auth).** Subject/Actor identity split for HTTP and gRPC; shared
  `security/identity` package; JWT service-actor mapping; gRPC metadata
  propagation; `FormatActorFromContext`; kit-doctor drift rule + autofix.
- **feat(auth).** OAuth access-token authenticator; unbound scoped API keys;
  `jwtutil.NormalizeSubjectID` for prefixed subject wire forms.

## v2.1.0 — 2026-06-10

Additive feature + fix release across the `/v2` module set (coordination tag
`release/v2.1.0`).

- **feat(apikey).** External / customer-facing API key support (issuance,
  hashing, lookup) with the `data/apikey/postgres` store and `app/apikey`
  Builder wiring.
- **fix(saga).** `runtime/saga` durable executor now resumes in-flight sagas
  concurrently with a bounded worker pool instead of serially.
- **chore(deps).** Workspace-wide minor/patch dependency bumps via Dependabot.

## v2.0.3 — 2026-06-08

Per-module patch. The `runtime` module is tagged at `runtime/v2.0.3` carrying
the saga concurrency fix ahead of the coordinated `v2.1.0` cut; not all modules
ship a `v2.0.3` tag.

## v2.0.2 — 2026-05-28

Patch release (coordination tag `release/v2.0.2`).

- **fix(resilience/bulkhead).** Decrement the in-flight counter before
  releasing the semaphore so concurrency accounting stays correct under load.
- **fix(data/cache).** `Delete` now calls `Wait()` like `Set` to drain the
  Ristretto buffer, so deletes are observable immediately.
- **chore(deps).** `golang.org/x/crypto` and `go-jose/v4` bumps; Dependabot
  enabled for version + security updates.
- **ci.** Dropped the CycloneDX SBOM workflow and `SUPPLY_CHAIN.md`; added a Go
  build cache.

## v2.0.1 — 2026-05-28

Release-engineering patch (coordination tag `release/v2.0.1`); no functional
code changes.

- Fixed stale `go.mod` pseudo-version pins so dependent modules resolve the
  versioned tags directly.
- Added a `check-tidy` gate to catch stale `go.mod` require lines.
- Allowlisted `go-jose/v4` (auth/oauth2 tests) and `gcpsm` / `gcpkms` for the
  `google.golang.org/api` boundary.

## v2.0.0 — 2026-05-12

The agentic-AI service backend release. Every `go.work` module is tagged at
`/v2` using Go semantic import versioning. Public API breaks vs. v1.x are
intentional — see [`docs/RELEASE_NOTES_v2.md`](docs/RELEASE_NOTES_v2.md) for
the full enumeration of breaking changes and the operational migration
sequence.

### Themes

- **Multi-tenant substrate.** Tenant-aware cache, idempotency, rate-limiter,
  and label-cardinality guard land as first-class primitives.
- **Cost and audit.** Per-tenant cost budgets (memory + Redis backends, inbound
  middleware, outbound `RoundTripper`), append-only signed action log, and an
  approval workflow.
- **MCP helpers.** Typed handlers as JSON-RPC tools with schema
  auto-generation; reuse the kit's full middleware stack.
- **Supply-chain hardening.** `govulncheck` + `osv-scanner`
  CI, direct dependency allowlist, heavy-SDK boundary gate, and threat
  model in `docs/audit/`. See
  [`docs/audit/THREAT_MODEL.md`](docs/audit/THREAT_MODEL.md).
- **Operational primitives.** RED metrics, Grafana dashboards (HTTP, gRPC, DB,
  Redis, Outbox, AMQP, rate-limit, storage), runbooks, `promtool` CI, and an
  operational-readiness coverage gate for every workspace module.
- **Crypto.** AWS KMS, Azure Key Vault, GCP KMS, and HashiCorp Vault Transit
  envelope-KEK adapters; PASETO; Argon2id password hashing; field encryption.
- **Credential rotation.** Provider-backed rotation hooks across pgx, Redis,
  AMQP, NATS, S3, Azure Blob, GCS, SFTP, CSRF, and signed HTTP requests, with
  bounded provider contexts where the kit owns startup/reconnect calls.
- **Builder integrations.** Golden-path `app.Builder` exposes every new
  primitive without per-service middleware wiring.
- **Breaking changes.** Background components are one-shot; manual lifecycle
  HTTP servers reject zero-value `http.Server`; Redis health checks are
  critical by default; ASVS registries / RED-metric default buckets / retry
  default policies are now accessors; CORS requires explicit origins; auth
  middleware fails closed; development-mode escape hatches removed. Full list
  in [`docs/RELEASE_NOTES_v2.md`](docs/RELEASE_NOTES_v2.md).

### Release engineering

- License finalized as **Apache 2.0**; `SECURITY.md` published at the repo
  root with the coordinated-disclosure policy.
- Release-candidate hardening: clarified release provenance, removed
  placeholder cryptographic material, added CODEOWNERS coverage for
  security-sensitive audit/release/workflow files, and trimmed completed
  audit artifacts from package docs.

See [`docs/RELEASE_NOTES_v2.md`](docs/RELEASE_NOTES_v2.md) for the full
enumeration of breaking changes, new primitives, and verification commands.

### Migration

For the per-symbol break list grouped by package (renames, signature changes,
removed APIs, wire-format changes), see
[`docs/RELEASE_NOTES_v2.md`](docs/RELEASE_NOTES_v2.md).
