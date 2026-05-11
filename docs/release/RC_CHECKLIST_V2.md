# rho-kit v2 Release Candidate Checklist

Baseline: commit `bfb475f` (`chore: harden rho-kit for v2 release`).

This file maps the release goal to concrete evidence. A v2.0.0 tag should not
be cut until every row is either passed with evidence or explicitly deferred in
the release notes and roadmap.

Snippet status: shell blocks in this checklist are executable from the
repository root unless a block says to `cd` into a module first.

## Prompt-To-Artifact Checklist

| Requirement | Artifact or command | Evidence standard | Current status |
|---|---|---|---|
| Current RC changes are isolated | `git status --short`, `git diff --stat` | Release-prep work is one coherent diff and not a mixed feature/hardening sweep. | Prepared 2026-05-11: diff is limited to release docs, repo-native release/CI tooling, `examples/agentic-service` test-harness address injection, and the `WithPostgres` diagnostic fix found by release-doc validation. |
| Public API review per module | [API_FREEZE_V2.md](API_FREEZE_V2.md), `go.work` | Every `go.work` module has a keep/remove/rename decision. | Passed 2026-05-11: `go list -m` coverage check produced no missing modules. |
| Golden-path sample apps compile and run | `examples/agentic-service`, `cmd/kit-new` scaffold tests | Example builds/tests; generated scaffold variants build in tests; local smoke run succeeds. | Passed 2026-05-11: example `go test`, example `go build`, `cmd/kit-new` tests, and local MCP `tools/list`/`echo`/budget smoke all succeeded. |
| Migration guide complete and validated | [MIGRATION_V2.md](MIGRATION_V2.md), [../RELEASE_NOTES_v2.md](../RELEASE_NOTES_v2.md), API grep checks | Breaking changes and adoption sequence are documented in one operational guide and named APIs exist in the current tree. | Passed 2026-05-11: import, safety opt-out, authz, Builder, DB migration, API-reshape, Redis health-check, and deferred-item migration paths are documented; API presence evidence is recorded in `MIGRATION_V2.md`. |
| Release notes complete | [../RELEASE_NOTES_v2.md](../RELEASE_NOTES_v2.md) | Notes include breaking changes, new primitives, deferred items, links to release artifacts, and are ready to paste into a future GitHub release. | Passed 2026-05-11: release notes link to release artifacts and cover breaking changes, shipped primitives, verification, and deferred work. |
| Docs snippets executable or illustrative | This checklist plus per-doc notes | Executable snippets are tied to tests or commands; recipe snippets are explicitly illustrative. | Passed 2026-05-11: markdown snippet sweep found every fenced-block document covered by a snippet-status note or explicit executable evidence. |
| Full gates pass | Commands below | test, race, lint, vulncheck, dependency allowlist, dependency boundaries, kit-doctor, diff check. | Passed 2026-05-11: all required RC commands completed successfully on the current tree. |
| Docker-backed integration tests pass where available | `go test -tags integration ./...` in split integration modules | Docker available: pass. Docker unavailable: record skip reason. | Passed 2026-05-11 with Docker 29.4.1: `make test-integration` completed successfully across workspace modules. |
| No unreviewed heavy deps in core modules | `make check-dependency-boundaries`, `make check-dependency-allowlist`, [../audit/dependency-allowlist.txt](../audit/dependency-allowlist.txt) | Both checks pass and allowlist is reviewed. | Passed 2026-05-11: boundary check reviewed 336 direct module edges; allowlist check reviewed 56 direct external deps. |
| No product-specific abstractions enter core | API freeze review, package decision tree in `AGENTS.md` and docs | New abstractions are generic platform primitives or isolated examples. | Passed 2026-05-11: API freeze keeps product-specific code isolated to the example surface; core modules remain reusable platform primitives. |
| Pre-tag publishability | `make check-publishable` | No internal modules are pinned at `v0.0.0`; internal replaces point at workspace modules; Go directives match `go.work`. | Passed 2026-05-11. |
| Workspace dependency release invariant | `EXPECTED_INTERNAL_VERSION=v2.0.0 make check-publishable` | Every internal `github.com/bds421/rho-kit/.../v2` require points at the version that will be tagged for every workspace module. Local `replace` directives do not count because downstream consumers ignore them. | Added 2026-05-11 as the repo-native lockstep release gate. |
| Dependency-aware release levels | `make release-plan`, `tools/plan-module-release.sh` | Internal `go.mod` requires are converted to dependency levels so modules that can be tagged together are explicit. Changed-mode reports modules changed since a base ref plus impacted dependents. | Added 2026-05-11: full v2 plan has 65 modules across five dependency levels. |
| Release-branch internal replace removal | `tools/drop-internal-replaces.sh`, `FORBID_INTERNAL_REPLACES=1 make check-publishable` | Final release branch drops local internal replaces before level tidies so `GOWORK=off go mod tidy` can write real internal checksums. | Future release-phase step documented; not run during preparation. |
| Local release rehearsal | `tools/rehearse-v2-release.sh` | Temporary clone and bare origin prove dependency-ordered tags, level tidies, downstream `go get`, `go.sum`, and command installs without touching real origin. | Passed 2026-05-11: [rehearsal log](rehearsals/20260511T100205Z-v2-release-rehearsal.log). |
| Downstream checksum proof is post-tag | Clean temporary consumer from [TAGGING_PLAN_V2.md](TAGGING_PLAN_V2.md) | Repository `go.sum` files for dependent levels are produced only after dependency levels are tagged; after all tags are pushed, a clean consumer must resolve selected modules and verify sums. | Updated 2026-05-11 after reviewing Go module checksum mechanics. |
| Future multi-module tag plan exists | [TAGGING_PLAN_V2.md](TAGGING_PLAN_V2.md) | Exact dependency-ordered tag strategy, commands, expected level count, and rollback are documented without creating tags now. | Prepared 2026-05-11: plan creates 65 module-prefixed tags across five dependency levels plus `release/v2.0.0` coordination tag later. |
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

The local rehearsal log at
`docs/release/rehearsals/20260511T100205Z-v2-release-rehearsal.log` shows 65
module tags created and pushed to a temporary bare origin across five levels,
selected v2 modules resolved by a clean consumer with real `go.sum` hashes, and
`cmd/kit-new`, `cmd/kit-migrate`, and `cmd/kit-doctor` installed from the
temporary `v2.0.0` tags.
