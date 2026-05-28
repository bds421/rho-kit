# Changelog

## Unreleased

_(no entries yet)_

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
