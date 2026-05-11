# rho-kit v2.0.0 release-excellence sweep — 2026-05-11

Snippet status: shell commands are executable from the repository root.

This pass treats "improve before 2.0" as a release-excellence audit, not a
new feature sprint. The goal is to remove contradictions, false assurances,
and small operational gaps that would make the library harder to trust after
tagging.

## Scope Reviewed

- Workspace surface: `go list -m` reports 65 modules, and
  `API_FREEZE_V2.md` covers the exact module set.
- Release tooling: `Makefile`, `.github/workflows/`, `tools/plan-module-release.sh`,
  `tools/check-publishable.sh`, `tools/rehearse-v2-release.sh`, and the final
  release/tagging runbooks.
- Docs and examples: `AGENTS.md`, `docs/ai/`, `docs/audit/`, `docs/release/`,
  `docs/RELEASE_NOTES_v2.md`, `examples/agentic-service`, and `cmd/kit-new`.
- Observability assets: Grafana dashboards and Prometheus rules under
  `observability/dashboards/`.

## Finding Classification

| ID | Classification | Finding | Resolution |
|---|---|---|---|
| RX-001 | Fix now | `SUPPLY_CHAIN.md` contained placeholder GPG key material and said the real key would be published before v2.0.0. Placeholder cryptographic material is worse than no key because it looks like a trust anchor. | Removed the placeholder, made GitHub Security Advisory the sensitive-report path, and documented that v2.0.0 does not publish a long-lived project GPG key. |
| RX-002 | Fix now | Release provenance docs described future Sigstore signing as if it were part of the v2.0.0 release model, but the actual workflow only validates readiness and the runbook performs manual dependency-ordered tagging. | Rewrote the provenance/key section around the actual release owner identity, GitHub workflow identity, SBOM workflow, and future keyless-attestation follow-up. |
| RX-003 | Fix now | `SUPPLY_CHAIN.md` claimed CODEOWNERS protection for security-sensitive policy docs, but the repository had no CODEOWNERS file. | Added `.github/CODEOWNERS` for supply-chain policy, threat model, dependency allowlist, release docs, workflows, and release gate scripts. |
| RX-004 | Fix now | Audit docs still said only HTTP/runtime/service overview dashboards shipped, while the tree includes gRPC RED, DB pool, Redis, Outbox, and Storage dashboards. | Refreshed `ROADMAP.md`, audit README, and the historical audit pointer; the remaining dashboard work is now limited to AMQP and rate-limit panels whose metric contracts are not frozen. |
| RX-005 | Fix now | `AGENTS.md` still described the repo as roughly 50 modules and did not surface the newer release gates. | Updated the module count and command list for dependency boundaries, publishability, and release-candidate gates. |
| RX-006 | Fix now | The observability recipe named metrics packages but did not tell consumers where the shipped dashboards/runbooks live or how to keep them synchronized with metric changes. | Added a dashboard/runbook section to `docs/ai/observability.md`. |
| RX-007 | Fix now | Anti-pattern scans found accidental `context.TODO()` and `http.DefaultClient` use in non-fixture tests, while the kit-doctor anti-pattern fixtures are intentional. | Replaced the accidental test uses with `context.Background()` and an explicit timeout-bearing client. |
| RX-008 | Reject for v2.0.0 | Large implementation-file refactors (`app/builder.go`, `security/jwtutil`, idempotency, NATS, queue/actionlog internals) would reduce line length but create public-surface and regression risk immediately before tag. | Leave code shape unchanged for v2.0.0; require a post-release refactor proposal with package-local benchmarks/tests before splitting. |
| RX-009 | Reject for v2.0.0 | Cloud KMS adapters, Kubernetes/etcd leader election, Kafka, extra scaffold token/module flags, and full per-package benchmarks are desirable but add dependencies, public API, or release process churn. | Keep them in the v2.1+ roadmap unless a concrete consumer blocker appears before tagging. |

No production behavior-changing code path was changed in this sweep. Focused
tests and release gates were still required because release docs, workflow
metadata, CODEOWNERS, and tests changed; the recorded commands below cover that
post-edit tree.

## Verification Plan

```bash
git diff --check
ruby -e 'require "yaml"; ARGV.each { |f| YAML.load_file(f) }' .github/workflows/*.yml
go test ./app ./cmd/kit-verify/...
make check-no-binaries
make check-dependency-boundaries
make check-dependency-allowlist
make check-publishable
EXPECTED_INTERNAL_VERSION=v2.0.0 make check-publishable
make release-plan
GOCACHE=/private/tmp/rho-kit-gocache go run ./cmd/kit-doctor -format=json -strict=critical .
make test
make lint
make build
make vulncheck
make test-race
make test-integration
make test-cover
make bench
tools/rehearse-v2-release.sh
```

## Verification Recorded

All commands above passed on 2026-05-11. The first `make vulncheck` attempt hit
a transient `vuln.go.dev` connection reset while fetching advisory metadata;
the immediate retry completed for every module with `No vulnerabilities found.`

Additional release invariants checked:

```bash
RELEASE_MODE=all make release-plan
git tag --list '*v2.0.0'
git ls-remote --tags origin '*v2.0.0'
```

The all-module release plan still covers 65 modules across five dependency
levels. The fresh local release rehearsal passed and wrote
`docs/release/rehearsals/20260511T180559Z-v2-release-rehearsal.log`. No local
or remote `*v2.0.0` tags existed after the rehearsal.
