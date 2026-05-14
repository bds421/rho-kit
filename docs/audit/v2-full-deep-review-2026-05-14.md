# rho-kit v2.0.0 Full Deep Review - 2026-05-14

Verdict: **YELLOW / do not tag yet**.

The implementation is much closer to release-ready than the earlier hostile
review state, but this pass still found release-blocking evidence drift and two
semantic issues that should be fixed before treating the v2 surface as stable.

## Scope And Method

Live workspace inventory was derived from `go.work`, not from stale prose:

- `go work edit -json | jq -r '.Use[].DiskPath' | wc -l`: 77 modules.
- `RELEASE_MODE=all make release-plan`: passed; 77 workspace modules, 77 selected modules, six dependency levels.
- Review-relevant tracked files counted at the start of the pass: 891 Go files and 128 docs/dashboard/release files.

Requested review angles covered in this pass:

- Release docs, runbooks, dashboards, and release gates against the current 77-module tree.
- Public-surface / exported-symbol inventory and module-coverage drift checks.
- Current examples and `cmd/kit-new` scaffold variants.
- New `infra/outbox/postgres` module and relay interaction.
- Lifecycle/shutdown, redaction, metrics, credential-rotation, and operational-readiness paths tied to recent changes.
- Parser/cursor/signed-request/pagination/URL/config-loader surfaces, including whether fuzz targets exist.

The tree was dirty during review and changed while the review was running.
Findings below reflect the final live state after rerunning focused checks.

## Command Evidence

Passing checks:

- `git diff --check`
- `make check-no-binaries`: `tracked-binary check OK (1215 files scanned)`
- `make check-dependency-allowlist`: `dependency allowlist check OK (59 direct external deps approved)`
- `make check-dependency-boundaries`: `heavy dependency boundary check OK (413 direct module edges checked)`
- `make check-dashboards`: Grafana JSON / Prometheus rule syntax passed.
- `make lint`: passed after a separate concurrent lint process released the golangci-lint lock.
- `make test`: passed on the live workspace.
- Focused uncached tests passed:
  - `go test -count=1 ./cmd/kit-new`
  - `(cd examples/agentic-service && go test -count=1 ./... && go build ./...)`
  - `(cd httpx && go test -count=1 ./middleware/signedrequest ./pagination ./urlutil ./sign)`
  - `(cd core && go test -count=1 ./config ./redact)`
  - `(cd data && go test -count=1 ./actionlog ./actionlog/memory ./approval ./approval/memory)`
  - `(cd observability && go test -count=1 ./auditlog)`
  - `(cd infra/outbox/postgres && go test -count=1 ./...)`
  - `(cd infra/leaderelection/pgadvisory && go test -count=1 ./...)`
  - `(cd infra/leaderelection/redislock && go test -count=1 ./...)`
  - `(cd infra/redis && go test -count=1 ./...)`
  - `(cd data/cache/rediscache && go test -count=1 ./...)`
  - `(cd observability/auditlog/postgres && go test -count=1 ./...)`
  - `(cd security && go test -count=1 ./netutil)`

Important caveats:

- `make check-operational-readiness` currently reports success, but the check is a false positive. See C-001.
- `rg -n "^func Fuzz|testing\\.F|fuzz" --glob "*_test.go"` found no Go fuzz targets in the workspace. See C-005.

## Must Fix Before Tag

### C-001 - Operational-readiness and API-freeze coverage gates are false positives

Severity: **HIGH / release blocker**

`tools/check-operational-readiness.sh` is intended to prove that every `go.work`
module has an operational review row. It does not currently prove that.

Evidence:

- `tools/check-operational-readiness.sh:33-40` enters each module directory and runs `go list -m -f '{{.Path}}'`.
- In workspace mode, `cd app/amqp && go list -m -f '{{.Path}}'` prints all 77 workspace modules, not only `app/amqp`.
- The script then calls `grep -Fq "| \`$module_path\` |"`, where `$module_path` contains newline-separated module paths. That lets one present module satisfy the grep and hides missing rows.
- Manual exact check failed:
  - `grep -F "| \`github.com/bds421/rho-kit/app/amqp/v2\` |" docs/release/OPERATIONAL_READINESS_V2.md` returned status 1.
- Manual `go.mod`-derived comparison shows both `docs/release/API_FREEZE_V2.md` and `docs/release/OPERATIONAL_READINESS_V2.md` miss:
  - `github.com/bds421/rho-kit/app/amqp/v2`
  - `github.com/bds421/rho-kit/app/grpc/v2`
  - `github.com/bds421/rho-kit/app/nats/v2`
  - `github.com/bds421/rho-kit/app/postgres/v2`
  - `github.com/bds421/rho-kit/app/redis/v2`
  - `github.com/bds421/rho-kit/app/tracing/v2`
- `docs/release/RC_CHECKLIST_V2.md:270-271` claims API-freeze coverage was checked and had no missing modules. That is false for the current tree.
- `docs/release/RC_CHECKLIST_V2.md:343-344` still says the operational check covered all 73 modules, while the current workspace has 77.
- `AGENTS.md:3` still says 73 Go modules.

Impact:

The release checklist can go green while six public adapter modules are missing
from the API-freeze and operational-readiness matrices.

Fix direction:

- Make the checker derive the module path from each `go.mod` directly, or run `GOWORK=off go list -m` per directory.
- Add exact rows for the six `app/*` adapter modules to API freeze and operational readiness.
- Update stale 73-module claims in `AGENTS.md` and `RC_CHECKLIST_V2.md`.
- Add the same exact module-coverage check for `API_FREEZE_V2.md`, not only operational readiness.

### C-002 - `signedrequest` still buffers the full body before MAC compare

Severity: **HIGH / security and resource-amplification risk**

The signed-request verifier now resolves the secret before reading the body,
which is an improvement, but it still buffers the full request body in memory
before comparing the MAC.

Evidence:

- `httpx/middleware/signedrequest/signedrequest.go:339-368` resolves the secret, then calls `streamBody`.
- `httpx/middleware/signedrequest/signedrequest.go:459-461` creates a `bytes.Buffer` and copies the whole limited body into it.
- `httpx/middleware/signedrequest/signedrequest.go:373-381` only compares the MAC after that buffer is complete.
- `httpx/middleware/signedrequest/signedrequest_test.go:588-595` says body bytes are streamed with "no full buffer until MAC passes", but the implementation does buffer before the MAC comparison.

Impact:

Any caller who can present a syntactically valid key ID can still force up to
`bodyMaxSize` memory per request before failing signature verification. Default
limit is 10 MiB. This is less broad than the old unauthenticated path, but it is
still not top-tier for a security middleware.

Fix direction:

- Stream the body hash while spooling the body to a bounded temp file or other bounded replay buffer.
- Only expose a replayable body to downstream after the MAC passes.
- Update the regression test so it proves the no-full-memory-buffer property rather than only resolver ordering.

### C-003 - Postgres outbox claim order does not satisfy the relay FIFO contract

Severity: **HIGH / correctness contract mismatch**

The relay documents that default serial publishing preserves FIFO ordering
across a batch, but the Postgres store does not guarantee returned row order.

Evidence:

- `infra/outbox/relay.go:361-368` says the serial fast path preserves FIFO ordering by iterating the returned batch in order.
- `infra/outbox/relay.go:176-186` documents default concurrency 1 as preserving FIFO-on-the-wire behavior.
- `infra/outbox/postgres/store.go:116-131` selects pending IDs ordered by `created_at`, then performs `UPDATE ... WHERE id IN (SELECT id FROM claimed) RETURNING ...`.
- SQL `UPDATE ... RETURNING` does not preserve the CTE `ORDER BY` unless the final returned result is explicitly ordered.
- Existing outbox Postgres integration tests cover `SKIP LOCKED`, stale state, backoff, heartbeat, and retention, but not returned claim order.

Impact:

Services relying on the default serial relay can publish out of insertion order
even though the public relay docs imply FIFO behavior.

Fix direction:

- Preserve order in `FetchPending`, for example by carrying an ordinal through the CTE and selecting from the updated rows ordered by that ordinal.
- Add an integration test inserting staggered `created_at` values and asserting `FetchPending` returns oldest-first.
- If strict FIFO is not intended for SQL stores, remove the FIFO claim from relay docs before freezing v2.

## Should Fix Before Tag

### C-004 - Prometheus contract freeze has syntax validation but no semantic contract gate

Severity: **MEDIUM / release-quality gap**

The dashboard/rule pack currently validates as JSON/YAML and Prometheus syntax,
but no gate proves that the dashboarded metric names and labels match collectors
registered by the code.

Evidence:

- `Makefile:121-127` runs `python3 -m json.tool` for dashboards and `promtool check rules` for Prometheus rules.
- `make check-dashboards` passed.
- `observability/dashboards/README.md:63-111` defines a stable metric-name and label contract.
- The sampled code-level collectors for outbox, AMQP, NATS, Redis Streams, Redis, storage, HTTP RED, and runtime metrics line up with the documented names, but this is manual evidence rather than a durable release gate.

Impact:

Prometheus contracts can drift after v2.0.0 without CI catching a renamed metric
or label mismatch, even though the release docs now treat the dashboarded metric
families as stable.

Fix direction:

- Add a metric-contract test that instantiates each dashboarded collector on a fresh registry and exports descriptor names + labels.
- Extract metric references from Grafana JSON and Prometheus rule YAML.
- Fail CI if dashboard/rule references are missing from the emitted descriptor set, with explicit allowlist support for external scrape labels such as `namespace`, `service`, `instance`, and `up`.

### C-005 - Parser/cursor/security surfaces have no fuzz targets

Severity: **MEDIUM / test-hardening gap**

The requested fuzz angle is currently missing as a class of tests.

Evidence:

- `rg -n "^func Fuzz|testing\\.F|fuzz" --glob "*_test.go"` returned no fuzz targets.
- Focused unit tests pass for the high-risk surfaces checked in this pass:
  `httpx/middleware/signedrequest`, `httpx/pagination`, `httpx/urlutil`,
  `httpx/sign`, `core/config`, `data/actionlog`, `data/approval`,
  `observability/auditlog`, and `security/netutil`.

Impact:

Malformed cursors, URLs, signed-request headers, canonical requests, pagination
tokens, and config env/file inputs are protected by unit tests but not by
mutation-style input exploration. That is weaker than the "top-tier Go library"
bar the release goal is aiming for.

Fix direction:

Add short, deterministic fuzz targets for:

- `actionlog.CursorSigner.Decode`
- `auditlog` cursor decode/query
- `approval` cursor decode/query
- `httpx/pagination` cursor parsing
- `signedrequest` header/canonical verification helpers
- `httpx.SafeRedirect`
- `security/netutil.SSRFSafe*FromURL` parsing/validation
- `core/config.Load` env and `_FILE` parsing boundaries

### C-006 - AGENTS decision tree has duplicate / conflicting outbox rows

Severity: **LOW / agent-doc usability**

Evidence:

- `AGENTS.md:194` routes "Transactional outbox (at-least-once messaging)" to `infra/outbox` + `infra/outbox/postgres`, but links to `docs/ai/observability.md`.
- `AGENTS.md:204` has another "Transactional outbox (DB + broker)" row for `infra/outbox`, linking to `docs/ai/messaging.md`.

Impact:

Agents and humans following the decision tree can pick the wrong recipe or miss
the Postgres module/migration path.

Fix direction:

Keep one outbox row, link it to `docs/ai/messaging.md`, and include
`infra/outbox/postgres` + `cmd/kit-migrate outbox`.

### C-007 - Outbox Postgres package comment names the wrong table

Severity: **LOW / documentation typo**

Evidence:

- `infra/outbox/postgres/store.go:17-20` says the store uses a single `audit_log_entries` table.
- The migration and implementation use `outbox_entries`.

Fix direction:

Change the comment to `outbox_entries`.

## Audited Clean In This Pass

- License metadata is now internally consistent in the live tree: `LICENSE.md`, `README.md`, `AGENTS.md`, and `RC_CHECKLIST_V2.md` all indicate Apache 2.0.
- `SECURITY.md` exists at the repo root and points reporters to GitHub Private Security Advisories.
- `cmd/kit-new` scaffold variants are compile/vet/kit-doctor tested by `cmd/kit-new/scaffold_test.go:229-369`, and the uncached scaffold test run passed.
- `examples/agentic-service` tests and builds in its own module.
- Startup redaction currently uses `redact.Error` and `redact.ErrorChain`; focused `app` and `core/redact` tests passed.
- Credential rotation support exists for the major runtime credentials reviewed:
  Postgres password provider + `Pool.Reset`, Redis go-redis credential providers,
  AMQP URL provider with timeout and reconnect use, NATS credential/token providers
  with timeout, S3 AWS credential providers/default chain, Azure token credential,
  SFTP password provider/key reload-on-reconnect, and hot TLS material through
  `Builder.WithReloadingTLS` / `netutil.Reloading*TLS`.
- Dashboard and alert syntax validates, and sampled code-level metric names/labels
  match the dashboarded families.
- The new auditlog Postgres and outbox Postgres modules have focused tests passing
  uncached in this tree.

## Remaining Release Gates Not Proven Here

- `make test-race`, `make test-cover`, `make bench`, `make bench-baseline`,
  `make vulncheck`, Docker-backed integration tests, and release rehearsal were
  not rerun in this pass.
- Existing release docs already mark some of those as stale or needing rerun;
  after fixing C-001 the docs should be revalidated by the corrected coverage gate.
