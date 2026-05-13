# rho-kit v2 Release Candidate Checklist

Baseline: commit `bfb475f` (`chore: harden rho-kit for v2 release`).

This file maps the release goal to concrete evidence. A v2.0.0 tag should not
be cut until every row is either passed with evidence or explicitly deferred in
the release notes.

Snippet status: shell blocks in this checklist are executable from the
repository root unless a block says to `cd` into a module first.

## Prompt-To-Artifact Checklist

| Requirement | Artifact or command | Evidence standard | Current status |
|---|---|---|---|
| Current RC changes are isolated | `git status --short`, `git diff --stat` | Release-prep work is one coherent diff and not a mixed feature/hardening sweep. | Prepared 2026-05-11: diff is release-candidate hardening across release docs/tooling, observability contract freeze, semantic default fixes, and their focused tests. |
| Public API review per module | [API_FREEZE_V2.md](API_FREEZE_V2.md), `go.work` | Every `go.work` module has a keep/remove/rename decision. | Passed 2026-05-11: `go list -m` coverage check produced no missing modules. |
| Golden-path sample apps compile and run | `examples/agentic-service`, `cmd/kit-new` scaffold tests | Example builds/tests; generated scaffold variants build in tests; local smoke run succeeds. | Passed 2026-05-11: example `go test`, example `go build`, `cmd/kit-new` tests, and local MCP `tools/list`/`echo`/budget smoke all succeeded. |
| Migration guide complete and validated | [MIGRATION_V2.md](MIGRATION_V2.md), [../RELEASE_NOTES_v2.md](../RELEASE_NOTES_v2.md), API grep checks | Breaking changes and adoption sequence are documented in one operational guide and named APIs exist in the current tree. | Passed 2026-05-11: import, safety opt-out, authz, Builder, DB migration, API-reshape, Redis health-check, and deferred-item migration paths are documented; API presence evidence is recorded in `MIGRATION_V2.md`. |
| Release notes complete | [../RELEASE_NOTES_v2.md](../RELEASE_NOTES_v2.md) | Notes include breaking changes, new primitives, deferred items, links to release artifacts, and are ready to paste into a future GitHub release. | Passed 2026-05-11: release notes link to release artifacts and cover breaking changes, shipped primitives, verification, and deferred work. |
| Semantic security/default review complete | Source, tests, and commit history | Fail-open defaults, audit metadata idempotency, auth bypass semantics, and misleading legacy APIs are reviewed before the API freeze. | Passed 2026-05-11/12: S2S auth checked fail-closed for missing permissions/scopes, approval idempotency preserves audit metadata, Redis per-feature health policy is explicit, MCP audited calls require actor attribution, and leader election term semantics are hardened. |
| Benchmark baselines captured | [benchmarks/v2.0.0/MANIFEST.md](benchmarks/v2.0.0/MANIFEST.md), `make bench-baseline` | Current benchmark suites have raw `go test -bench` outputs that can be used as `kit-bench-gate -baseline` inputs. | Captured 2026-05-12: `core`, `crypto`, `data`, `httpx`, `resilience`, and `runtime` baseline files exist with `-count=5`, including the added tenant, envelope, rate-limit, and middleware-chain hot-path benchmarks. |
| Docs snippets executable or illustrative | This checklist plus per-doc notes | Executable snippets are tied to tests or commands; recipe snippets are explicitly illustrative. | Passed 2026-05-11: markdown snippet sweep found every fenced-block document covered by a snippet-status note or explicit executable evidence. |
| Full gates pass | Commands below | test, race, lint, vulncheck, dependency allowlist, dependency boundaries, dashboard/rule validation, benchmark baseline capture, coverage, benchmarks, kit-doctor, diff check. | Partial refresh 2026-05-12: diff, test, lint, build, dependency, dashboard, publishability, release-plan, kit-doctor, benchmark-baseline, and Azure Key Vault module race/vulncheck gates passed on the 67-module tree. Full workspace race, coverage, benchmark, Docker integration, and release rehearsal remain to rerun before tagging. |
| Docker-backed integration tests pass where available | `go test -tags integration ./...` in split integration modules | Docker available: pass. Docker unavailable: record skip reason. | Blocked 2026-05-12: `docker info` hung before the first module; the stuck process was terminated. Last successful full Docker run remains 2026-05-11 with Docker 29.4.1. |
| No unreviewed heavy deps in core modules | `make check-dependency-boundaries`, `make check-dependency-allowlist`, [../audit/dependency-allowlist.txt](../audit/dependency-allowlist.txt) | Both checks pass and allowlist is reviewed. | Passed 2026-05-13 on live workspace: boundary check reviewed 348 direct module edges; allowlist check reviewed 59 direct external deps including Vault API, Azure Key Vault keys, and NATS Prometheus metrics. |
| Security-sensitive files have review ownership | [.github/CODEOWNERS](../../.github/CODEOWNERS), [../audit/SUPPLY_CHAIN.md](../audit/SUPPLY_CHAIN.md), `make check-release-team` | Supply-chain policy, threat model, dependency allowlist, release docs, workflows, and release gate scripts route to the security owner. The team and branch protection are verified by `make check-release-team` (added 2026-05-13). | Present for security-sensitive package and release files. Branch-protection enforcement of CODEOWNERS reviews and existence of the `@bds421/security` GitHub team remain open; `make check-release-team` will fail loudly at the runbook's preflight step if either is missing. |
| License finalized | [`LICENSE.md`](../../LICENSE.md) | Repository carries a complete, dated copyright/license header consistent with the chosen license. | DONE 2026-05-12: Apache 2.0 chosen, `Copyright 2026 BDS421` line populated. |
| SECURITY.md present at repo root | [`SECURITY.md`](../../SECURITY.md) | Public, coordinated-disclosure security policy is published at the repo root and references the supply-chain SLA. | DONE 2026-05-13: `SECURITY.md` directs all reports to GitHub Private Security Advisories (no email mailbox) and quotes the SLA from `docs/audit/SUPPLY_CHAIN.md`. `docs/audit/SUPPLY_CHAIN.md` §9 was aligned to the same GHSA-only channel. |
| No product-specific abstractions enter core | API freeze review, package decision tree in `AGENTS.md` and docs | New abstractions are generic platform primitives or isolated examples. | Passed 2026-05-11: API freeze keeps product-specific code isolated to the example surface; core modules remain reusable platform primitives. |
| Pre-tag publishability | `make check-publishable` | No internal modules are pinned at `v0.0.0`; internal replaces point at workspace modules; Go directives match `go.work`. | Passed 2026-05-11. |
| Workspace dependency release invariant | `EXPECTED_INTERNAL_VERSION=v2.0.0 make check-publishable` | Every internal `github.com/bds421/rho-kit/.../v2` require points at the version that will be tagged for every workspace module. Local `replace` directives do not count because downstream consumers ignore them. | Added 2026-05-11 as the repo-native lockstep release gate. |
| Dependency-aware release levels | `make release-plan`, `tools/plan-module-release.sh` | Internal `go.mod` requires are converted to dependency levels so modules that can be tagged together are explicit. Changed-mode reports modules changed since a base ref plus impacted dependents. | Passed 2026-05-12: `RELEASE_MODE=all make release-plan` reports 67 modules across five dependency levels including `crypto/envelope/vaulttransit` and `crypto/envelope/azurekeyvault`. |
| Release-branch internal replace removal | `tools/drop-internal-replaces.sh`, `FORBID_INTERNAL_REPLACES=1 make check-publishable` | Final release branch drops local internal replaces before level tidies so `GOWORK=off go mod tidy` can write real internal checksums. | Future release-phase step documented; not run during preparation. |
| Local release rehearsal | `tools/rehearse-v2-release.sh` | Temporary clone and bare origin prove dependency-ordered tags, level tidies, downstream `go get`, `go.sum`, and command installs without touching real origin. | Passed 2026-05-13 on the 67-module tree (5 dependency levels: 7/8/6/25/21). Downstream consumer resolved every module at `v2.0.0` and command installs of `cmd/kit-new`, `cmd/kit-migrate`, and `cmd/kit-doctor` succeeded. Log written to `docs/release/rehearsals/20260513T050809Z-v2-release-rehearsal.log` (gitignored as a local evidence artifact). |
| Downstream checksum proof is post-tag | Clean temporary consumer from [TAGGING_PLAN_V2.md](TAGGING_PLAN_V2.md) | Repository `go.sum` files for dependent levels are produced only after dependency levels are tagged; after all tags are pushed, a clean consumer must resolve selected modules and verify sums. | Updated 2026-05-11 after reviewing Go module checksum mechanics. |
| Future multi-module tag plan exists | [TAGGING_PLAN_V2.md](TAGGING_PLAN_V2.md) | Exact dependency-ordered tag strategy, commands, expected level count, and rollback are documented without creating tags now. | Updated 2026-05-12: plan creates 67 module-prefixed tags across five dependency levels plus `release/v2.0.0` coordination tag later. |
| Future final release runbook exists | [FINAL_RELEASE_RUNBOOK_V2.md](FINAL_RELEASE_RUNBOOK_V2.md) | Exact future commands, expected outputs, stop conditions, release notes source, and rollback are documented. | Prepared 2026-05-11. |
| No tag or GitHub release created during preparation | `git tag --list '*v2.0.0'`, `git ls-remote --tags origin '*v2.0.0'`, `gh release view release/v2.0.0` | Preparation phase must not tag or publish. | Verified 2026-05-11 before final audit: no local or remote `*v2.0.0` tags existed, `gh` reported `release not found`, and no tag/push/GitHub-release creation commands were run. |

## Required RC Commands

Run from the repository root:

```bash
git diff --check
make test
make lint
make check-dependency-boundaries
make check-dependency-allowlist
make check-publishable
make check-dashboards
EXPECTED_INTERNAL_VERSION=v2.0.0 make check-publishable
make release-plan
GOCACHE=/private/tmp/rho-kit-gocache go run ./cmd/kit-doctor -format=json -strict=critical .
make vulncheck
make test-race
make test-integration
```

`make test-integration` requires Docker and runs from each workspace module root
so split integration modules are tested in their own module context.

## Docs Snippet Classification

The documentation has three snippet classes:

| Class | Meaning | Evidence |
|---|---|---|
| Executable command | Shell command intended to run as written from the documented working directory. | Covered by RC commands or by a named manual smoke run. |
| Compile-tested artifact | Code generated or imported by tests, examples, or scaffold builds. | Covered by `make test`, `make test-race`, and `cmd/kit-new` scaffold tests. |
| Illustrative fragment | Partial Go, PromQL, JSON, or configuration excerpt intended to show shape, not stand alone. | Must be introduced by wording such as "illustrative", "shape", "example", "fragment", or by a document-level snippet-status note. |

Recipe docs under `docs/ai/*.md` are illustrative fragments unless a block is
inside an explicit "Run" or "Command" section. The executable downstream paths
for the golden path are `examples/agentic-service` and `cmd/kit-new` generated
scaffold tests.

## Golden-Path Smoke Evidence To Record

Record the exact command output before tagging:

```bash
GOCACHE=/private/tmp/rho-kit-gocache go test ./examples/agentic-service/...
GOCACHE=/private/tmp/rho-kit-gocache go build ./examples/agentic-service/...
```

Manual local smoke, if desired:

```bash
cd examples/agentic-service
export AGENTIC_SERVICE_DEMO_TOKEN="$(openssl rand -base64 32)"
go run ./cmd/agentic-service
```

Then POST the `tools/list` and `echo` requests from the example README.

## Evidence Recorded 2026-05-11

The following commands passed from the repository root on the current tree:

```bash
git diff --check
make test
make lint
make check-dependency-boundaries
make check-dependency-allowlist
make check-publishable
make check-dashboards
EXPECTED_INTERNAL_VERSION=v2.0.0 make check-publishable
make release-plan
GOCACHE=/private/tmp/rho-kit-gocache go run ./cmd/kit-doctor -format=json -strict=critical .
make vulncheck
make test-race
make test-integration
```

Supporting golden-path commands passed:

```bash
go test ./examples/agentic-service/...
GOCACHE=/private/tmp/rho-kit-gocache go build ./examples/agentic-service/...
GOCACHE=/private/tmp/rho-kit-gocache go test ./cmd/kit-new/...
```

Manual local smoke for `examples/agentic-service` returned a valid MCP
`tools/list` response containing the `echo` tool and a valid `echo` response:
`{"echoed":"hi"}`. The documented budget endpoint returned:
`{"remaining":1000,"tenant":"acme"}`.

The RC runs exposed four release-candidate blockers that
were fixed before recording pass status:

- AMQP integration tests now opt in to local plaintext RabbitMQ through a
  test-only helper, preserving the production `amqps`/TLS default.
- Redis integration tests now assert that `HealthCheck` is critical by default
  and cover `NonCriticalHealthCheck` separately.
- `examples/agentic-service` keeps the exported `Run` binding to documented
  `:8080`, but tests now call an unexported address-injected runner so the
  shutdown smoke can bind `127.0.0.1:0`.
- Release-facing docs and app diagnostics now consistently name
  `WithPostgres`; release-prep API validation caught stale removed Builder
  aliases before publishing.

Post release-prep validation after adding the tagging/runbook artifacts and the
`WithPostgres` diagnostic fix:

```bash
git diff --check
go test ./app ./examples/agentic-service/... ./cmd/kit-new/...
(cd app && go test -race ./...)
(cd app && go test -tags integration ./...)
go build ./examples/agentic-service/...
(cd app && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.10.1 run --timeout=5m)
make check-dependency-boundaries
make check-dependency-allowlist
make check-publishable
make check-dashboards
EXPECTED_INTERNAL_VERSION=v2.0.0 make check-publishable
GOCACHE=/private/tmp/rho-kit-gocache go run ./cmd/kit-doctor -format=json -strict=critical .
```

All commands passed on 2026-05-11. The snippet-status sweep returned no
uncovered fenced-block documents, `git tag --list '*v2.0.0'` returned no local
tags, and `gh release view release/v2.0.0 --repo bds421/rho-kit` returned
`release not found`.

Post release-prep validation after removing the old JavaScript workspace
orchestration files and replacing them with repo-native checks:

```bash
git diff --check
bash -n tools/check-publishable.sh
ruby -e 'require "yaml"; ARGV.each { |f| YAML.load_file(f) }' .github/workflows/*.yml
make check-no-binaries
make check-dependency-boundaries
make check-dependency-allowlist
make check-publishable
make check-dashboards
EXPECTED_INTERNAL_VERSION=v2.0.0 make check-publishable
go test ./app ./examples/agentic-service/... ./cmd/kit-new/...
go build ./examples/agentic-service/...
make ci
bash -n tools/plan-module-release.sh
bash -n tools/drop-internal-replaces.sh
bash -n tools/rehearse-v2-release.sh
tools/plan-module-release.sh -mode all -format tsv
tools/plan-module-release.sh -mode changed -base HEAD~1
tools/rehearse-v2-release.sh
```

All commands passed on 2026-05-11. `make ci` exercised the new root Makefile
path: binary check, dependency allowlist, dependency boundaries,
publishability, lint, race tests, and workspace builds. A focused text scan
found no remaining JavaScript workspace orchestration references; the only
remaining uppercase token matches are Redis lock documentation and one checksum
substring in `go.sum`.

Important release-mechanics correction: the release runbook now uses
dependency-ordered module levels. Dependency levels are tagged first; dependent
levels are tidied with `GOWORK=off` only after those dependency tags exist, so
their committed `go.sum` files can contain real internal module checksums. The
runbook still requires a clean downstream consumer check after all tags are
pushed and before publishing the GitHub release.

Release-prep validation after removing placeholder cryptographic material,
adding CODEOWNERS, refreshing dashboard/release docs, and cleaning minor test
anti-patterns used this command set:

```bash
git diff --check
ruby -e 'require "yaml"; ARGV.each { |f| YAML.load_file(f) }' .github/workflows/*.yml
go test ./app ./cmd/kit-verify/...
make check-no-binaries
make check-dependency-boundaries
make check-dependency-allowlist
make check-publishable
make check-dashboards
EXPECTED_INTERNAL_VERSION=v2.0.0 make check-publishable
make release-plan
GOCACHE=/private/tmp/rho-kit-gocache go run ./cmd/kit-doctor -format=json -strict=critical .
make test
make lint
make build
make test-race
make test-integration
make vulncheck
make test-cover
make bench
make bench-baseline
RELEASE_MODE=all make release-plan
tools/rehearse-v2-release.sh
git tag --list '*v2.0.0'
git ls-remote --tags origin '*v2.0.0'
```

All commands passed on 2026-05-11. The first `make vulncheck` attempt hit a
transient `vuln.go.dev` connection reset while fetching advisory metadata; the
immediate retry completed for every module with `No vulnerabilities found.`
The rehearsal evidence from that date predates the
`crypto/envelope/vaulttransit` and `crypto/envelope/azurekeyvault` modules and
must be refreshed before tagging. No
local or remote `*v2.0.0` tags existed after the rehearsal.

2026-05-12 follow-up for Vault Transit, Azure Key Vault, benchmark baselines,
and provider dashboards refreshed the following non-Docker gates on the
67-module tree:

```bash
git diff --check
make check-dependency-allowlist
make check-dependency-boundaries
make check-dashboards
make check-publishable
EXPECTED_INTERNAL_VERSION=v2.0.0 make check-publishable
RELEASE_MODE=all make release-plan
GOCACHE=/private/tmp/rho-kit-gocache go run ./cmd/kit-doctor -format=json -strict=critical .
make test
make lint
make build
make check-no-binaries
make bench-baseline
cd crypto/envelope/azurekeyvault && go test -race ./...
cd crypto/envelope/azurekeyvault && go run golang.org/x/vuln/cmd/govulncheck@v1.1.4 ./...
cd crypto/envelope/azurekeyvault && go mod verify
```

All commands above passed. API freeze coverage was also rechecked with `go
list -m` against `API_FREEZE_V2.md` and produced no missing or extra modules.
The earlier full `make test-race`, `make test-cover`, and `make bench` evidence
predates the 67th Azure Key Vault module; rerun those full gates before
tagging if this remains the final candidate. `make test-integration` did not
reach tests because `docker info` hung before the first module; the stuck
Docker check was terminated and must be rerun on a responsive Docker daemon
before tagging.

2026-05-13 follow-up for freezing the direct NATS JetStream and Redis Streams
Prometheus contracts refreshed the focused metrics gates:

```bash
git diff --check
GOCACHE=/private/tmp/rho-kit-gocache go test ./app/...
cd infra/messaging/natsbackend && GOCACHE=/private/tmp/rho-kit-gocache go test ./...
cd data/stream/redisstream && GOCACHE=/private/tmp/rho-kit-gocache go test ./...
cd infra/messaging/redisbackend && GOCACHE=/private/tmp/rho-kit-gocache go test ./...
make check-dashboards
make check-no-binaries
make check-dependency-boundaries
make check-publishable
```

All commands above passed. The dashboard pack now includes NATS JetStream and
Redis Streams dashboards, recording rules, alerts, and runbooks. A clean
detached worktree of the committed metrics changes also passed
`bash tools/check-direct-dependency-allowlist.sh` with 59 approved direct
external dependencies. The live workspace allowlist caveat from
`runtime/temporal/go.mod` was resolved by narrowing the Temporal worker test
seam so `github.com/nexus-rpc/sdk-go` remains only an indirect dependency of
`go.temporal.io/sdk`; the live `make check-dependency-allowlist` run now
passes with 59 approved direct external dependencies.

2026-05-13 follow-up for credential-rotation support refreshed the broad
non-Docker gates after adding provider-backed rotation APIs for pgx, Redis,
AMQP, NATS, S3, Azure Blob, GCS, SFTP, CSRF, and signed requests:

```bash
git diff --check
make test
make lint
make vulncheck
make check-dependency-allowlist
make check-dependency-boundaries
```

All commands above passed. Focused changed-module tests also passed for
`security/csrf`, `httpx/sign`, `httpx/middleware/csrf`, `infra/sqldb/pgx`,
`app`, `infra/messaging/amqpbackend`, `infra/messaging/natsbackend`,
`infra/storage/s3backend`, `infra/storage/azurebackend`,
`infra/storage/gcsbackend`, and `infra/storage/sftpbackend`.
