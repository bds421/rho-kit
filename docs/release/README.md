# rho-kit v2 Release Artifacts

This directory contains the release-candidate artifacts that are meant to be
read immediately before tagging v2.0.0. Historical audits and threat models
remain under `docs/audit/`; this directory is the operational release view.

| File | Purpose |
|---|---|
| [API_FREEZE_V2.md](API_FREEZE_V2.md) | Per-module keep/remove/rename decision record for the v2 public surface. |
| [MIGRATION_V2.md](MIGRATION_V2.md) | Downstream migration guide from v1.x / pre-RC v2 code to the v2.0.0 contract. |
| [RC_CHECKLIST_V2.md](RC_CHECKLIST_V2.md) | Prompt-to-artifact release checklist with evidence commands and remaining RC checks. |

The release notes in [../RELEASE_NOTES_v2.md](../RELEASE_NOTES_v2.md) are the
consumer-facing changelog. The files here explain why the public surface is
frozen and how to prove the tag is ready.
