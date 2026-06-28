# Changelog

## Unreleased

_(no entries yet)_

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
