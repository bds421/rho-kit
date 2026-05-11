Read and follow AGENTS.md in this repository root. It is the canonical AI
agent guide for this project, including build commands, package decision tree,
golden path, conventions, and links to detailed recipe files in docs/ai/.

## Workspace And Release Prep

This repository is a Go multi-module workspace. Use `go.work` as the source of
truth for module membership and the root `Makefile` for routine checks.

Common commands:

```bash
make test
make test-race
make test-integration
make lint
make build
make check-dependency-allowlist
make check-dependency-boundaries
make check-publishable
make release-plan
```

Release preparation for v2.0.0 is documented under `docs/release/`.
Preparation is not permission to tag or publish. Future release owners should
follow `docs/release/FINAL_RELEASE_RUNBOOK_V2.md` and
`docs/release/TAGGING_PLAN_V2.md` only after explicitly entering the tagging
phase.

For workspace dependencies, compute the dependency levels before tagging:

```bash
RELEASE_MODE=all make release-plan
EXPECTED_INTERNAL_VERSION=v2.0.0 make check-publishable
```

The final release branch must also drop local internal `replace` directives and
pass `FORBID_INTERNAL_REPLACES=1 EXPECTED_INTERNAL_VERSION=v2.0.0 make
check-publishable`. Downstream Go consumers resolve the versions in each
module's `require` lines; dependent module `go.sum` entries should be generated
after dependency-level tags exist.
