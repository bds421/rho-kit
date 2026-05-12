# rho-kit v2 Release Artifacts

This directory contains the release-candidate artifacts that are meant to be
read immediately before tagging v2.0.0. Security policy documents remain under
`docs/audit/`; this directory is the operational release view.

| File | Purpose |
|---|---|
| [API_FREEZE_V2.md](API_FREEZE_V2.md) | Per-module keep/remove/rename decision record for the v2 public surface. |
| [MIGRATION_V2.md](MIGRATION_V2.md) | Downstream migration guide from v1.x / pre-RC v2 code to the v2.0.0 contract. |
| [RC_CHECKLIST_V2.md](RC_CHECKLIST_V2.md) | Prompt-to-artifact release checklist with evidence commands and remaining RC checks. |
| [TAGGING_PLAN_V2.md](TAGGING_PLAN_V2.md) | Future dependency-ordered multi-module tag strategy and exact tag commands. |
| [FINAL_RELEASE_RUNBOOK_V2.md](FINAL_RELEASE_RUNBOOK_V2.md) | Future final-release procedure with stop conditions, expected outputs, and rollback steps. |

The release notes in [../RELEASE_NOTES_v2.md](../RELEASE_NOTES_v2.md) are the
consumer-facing GitHub release body. The files here explain why the public
surface is frozen, how to prove the tag is ready, and how to cut the future
release without improvising.

Workspace dependency release readiness is planned with `make release-plan` and
checked with `EXPECTED_INTERNAL_VERSION=v2.0.0 make check-publishable`. This
matters because local `replace` directives are ignored by downstream consumers;
the published module tags must match the internal `require` versions.

The final release branch must drop local internal replaces and tag modules in
dependency levels. Each dependent level is tidied only after its dependency
level tags exist, so committed `go.sum` files record real internal checksums.
The final runbook also verifies resolution from a clean temporary downstream
module before publishing the GitHub release.

Run `tools/rehearse-v2-release.sh` before touching the real remote. It executes
the dependency-ordered release against a temporary bare repository and writes a
local-only log under `docs/release/rehearsals/`; those logs are evidence
artifacts and are not tracked.
