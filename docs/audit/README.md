# rho-kit Security Documents

This directory holds package-relevant security policy documents and the direct
dependency allowlist used by CI. Completed audit reports, review notes,
planning notes, and rehearsal evidence are not kept as package documentation; use
`git log` for that historical trail.

Release-candidate artifacts live in [../release](../release/). Use those files
for API-freeze, migration, and final tag evidence.

## Documents

| File | Purpose |
|---|---|
| [THREAT_MODEL.md](THREAT_MODEL.md) | STRIDE-style threat surface; assets, adversaries, mitigations, shipped gap closures, and remaining follow-up list. Updated whenever a new threat ID lands. |
| [dependency-allowlist.txt](dependency-allowlist.txt) | Exact review ledger for direct external Go module dependencies; enforced by `make check-dependency-allowlist`. |

Supply-chain policy lives in the code that enforces it:
- `tools/check-direct-dependency-allowlist.sh` — direct-dep allowlist gate
- `tools/check-heavy-dependency-boundaries.sh` — adapter-boundary gate
- `tools/check-licenses.sh` — license allowlist gate
- `.github/workflows/supply-chain.yml` — runs the gates on PR/push + weekly cron

## How Findings Flow Now

1. New threats land in [THREAT_MODEL.md](THREAT_MODEL.md) under the
   relevant §4 sub-section (or a new sub-section if none fits) plus
   the §8 gap list if no in-kit mitigation exists yet.
2. Implementation work is tracked in conventional-commit messages
   and PR descriptions, not in dated markdown artifacts.
3. The vulnerability-response SLO in [`../../SECURITY.md`](../../SECURITY.md)
   governs HIGH/CRITICAL response time.
