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
| Public API review per module | [API_FREEZE_V2.md](API_FREEZE_V2.md), `go.work` | Every `go.work` module has a keep/remove/rename decision. | Passed 2026-05-11: `go list -m` coverage check produced no missing modules. |
| Golden-path sample apps compile and run | `examples/agentic-service`, `cmd/kit-new` scaffold tests | Example builds/tests; generated scaffold variants build in tests; local smoke run succeeds. | Passed 2026-05-11: example `go test`, example `go build`, `cmd/kit-new` tests, and local MCP `tools/list`/`echo` smoke all succeeded. |
| Migration guide complete | [MIGRATION_V2.md](MIGRATION_V2.md), [../RELEASE_NOTES_v2.md](../RELEASE_NOTES_v2.md) | Breaking changes and adoption sequence are documented in one operational guide. | Passed 2026-05-11: import, safety opt-out, authz, Builder, DB migration, API-reshape, Redis health-check, and deferred-item migration paths are documented. |
| Release notes complete | [../RELEASE_NOTES_v2.md](../RELEASE_NOTES_v2.md) | Notes include breaking changes, new primitives, deferred items, and links to release artifacts. | Passed 2026-05-11: release notes link to release artifacts and cover breaking changes, shipped primitives, verification, and deferred work. |
| Docs snippets executable or illustrative | This checklist plus per-doc notes | Executable snippets are tied to tests or commands; recipe snippets are explicitly illustrative. | Passed 2026-05-11: markdown snippet sweep found every fenced-block document covered by a snippet-status note or explicit executable evidence. |
| Full gates pass | Commands below | test, race, lint, vulncheck, dependency allowlist, dependency boundaries, kit-doctor, diff check. | Passed 2026-05-11: all required RC commands completed successfully on the current tree. |
| Docker-backed integration tests pass where available | `go test -tags integration ./...` in split integration modules | Docker available: pass. Docker unavailable: record skip reason. | Passed 2026-05-11 with Docker 29.4.1: `make test-integration` completed successfully across workspace modules. |
| No unreviewed heavy deps in core modules | `make check-dependency-boundaries`, `make check-dependency-allowlist`, [../audit/dependency-allowlist.txt](../audit/dependency-allowlist.txt) | Both checks pass and allowlist is reviewed. | Passed 2026-05-11: boundary check reviewed 336 direct module edges; allowlist check reviewed 56 direct external deps. |
| No product-specific abstractions enter core | API freeze review, package decision tree in `AGENTS.md` and docs | New abstractions are generic platform primitives or isolated examples. | Passed 2026-05-11: API freeze keeps product-specific code isolated to the example surface; core modules remain reusable platform primitives. |

## Required RC Commands

Run from the repository root:

```bash
git diff --check
GOCACHE=/private/tmp/rho-kit-gocache make test
make lint
make check-dependency-boundaries
make check-dependency-allowlist
GOCACHE=/private/tmp/rho-kit-gocache go run ./cmd/kit-doctor -format=json -strict=critical .
GOCACHE=/private/tmp/rho-kit-gocache make vulncheck
GOCACHE=/private/tmp/rho-kit-gocache make test-race
GOCACHE=/private/tmp/rho-kit-gocache make test-integration
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
GOCACHE=/private/tmp/rho-kit-gocache make test
make lint
make check-dependency-boundaries
make check-dependency-allowlist
GOCACHE=/private/tmp/rho-kit-gocache go run ./cmd/kit-doctor -format=json -strict=critical .
GOCACHE=/private/tmp/rho-kit-gocache make vulncheck
GOCACHE=/private/tmp/rho-kit-gocache make test-race
GOCACHE=/private/tmp/rho-kit-gocache make test-integration
```

Supporting golden-path commands passed:

```bash
GOCACHE=/private/tmp/rho-kit-gocache go test ./examples/agentic-service/...
GOCACHE=/private/tmp/rho-kit-gocache go build ./examples/agentic-service/...
GOCACHE=/private/tmp/rho-kit-gocache go test ./cmd/kit-new/...
```

Manual local smoke for `examples/agentic-service` returned a valid MCP
`tools/list` response containing the `echo` tool and a valid `echo` response:
`{"echoed":"hi"}`.

The integration RC run exposed two release-candidate test-harness blockers that
were fixed before recording pass status:

- AMQP integration tests now opt in to local plaintext RabbitMQ through a
  test-only helper, preserving the production `amqps`/TLS default.
- Redis integration tests now assert that `HealthCheck` is critical by default
  and cover `NonCriticalHealthCheck` separately.
