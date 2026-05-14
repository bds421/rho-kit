# rho-kit supply-chain policy — v2.0.0

> **Status:** living document. Companion to
> [THREAT_MODEL.md](THREAT_MODEL.md). The threat model covers
> attacks on a *running* service that uses the kit; this document
> covers attacks on the *path the kit's code takes* from source to
> running service — dependencies, signing, build reproducibility,
> CVE response, and provenance.

Snippet status: shell blocks in this policy are executable from the repository
root unless the surrounding section names a different working directory. Go
blocks are illustrative module-layout fragments.

A "trusted library" claim has to mean something verifiable.
Trusted-by-marketing is the same as untrusted in any audit. The
policies below state, in concrete terms, what the kit promises and
how to verify each promise.

---

## Table of contents

1. [Dependency-pinning policy](#1-dependency-pinning-policy)
2. [`replace` directives and intra-repo deps](#2-replace-directives-and-intra-repo-deps)
3. [Update cadence (Dependabot)](#3-update-cadence-dependabot)
4. [Reproducible builds](#4-reproducible-builds)
5. [SBOMs (CycloneDX)](#5-sboms-cyclonedx)
6. [Release provenance and key rotation](#6-release-provenance-and-key-rotation)
7. [Vulnerability response SLO](#7-vulnerability-response-slo)
8. [Allowed licenses + CI verification](#8-allowed-licenses--ci-verification)
9. [Security contact and private reports](#9-security-contact-and-private-reports)
10. [Audit trail of policy changes](#10-audit-trail-of-policy-changes)

---

## 1. Dependency-pinning policy

### 1.1 Required form for every Go module

Every `go.mod` in the workspace MUST satisfy all of:

- `go` directive pinned to an exact patch version (e.g.
  `go 1.26.2`, never `go 1.26`).
- The workspace `go.work` `toolchain` directive pins the exact
  patch version (e.g. `toolchain go1.26.2`) for every module in the
  workspace, so per-module `toolchain` directives are intentionally
  omitted to avoid drift between the workspace and individual
  `go.mod` files. Downstream consumers receive the resolved
  toolchain via the workspace pin.
- Every `require` line uses an exact semver tag — never `v0.0.0-`
  pseudo-versions for external code, never `latest`, never a
  branch reference.
- Every `require` line for an intra-repo module is paired with a
  `replace` directive (see §2).
- Every entry in `go.sum` is preserved; deletions only happen via
  `go mod tidy` after a deliberate version change.

The CI pipeline (`.github/workflows/ci.yml`) runs the root Makefile
gates, including dependency policy checks and workspace builds. A
missing checksum or floating version surfaces as a build or policy
failure.

### 1.2 Why exact tags

Pseudo-versions (`v0.0.0-20260101...-abc123def`) are tempting for
"just one fix not in a tagged release". They are forbidden because:

- They bypass the upstream maintainer's release gate (no tag, no
  intent to release).
- They bypass `govulncheck`'s tag-based affected-version matching
  (see [vuln.yml](../../.github/workflows/vuln.yml)).
- They make SBOM diffs noisy — every CI run produces a slightly
  different `purl` if the pseudo-version updates.

Exception: a release-owner scratch branch may briefly contain
pseudo-versions while preparing a local experiment. Those versions
must never reach `main` and must not appear in the release commit.

### 1.3 Module-graph constraint

The kit ships many Go modules sharing a single `go.work`. For a release such
as v2.0.0, every module that depends on `crypto/passhash` (for example) MUST
reference a version that is already tagged before the dependent module is
tagged. The release owner computes that order with:

```bash
RELEASE_MODE=all make release-plan
```

The final release branch also enforces the version and local-replace
invariants with:

```bash
FORBID_INTERNAL_REPLACES=1 EXPECTED_INTERNAL_VERSION=v2.0.0 make check-publishable
```

A divergent version pin (e.g. `app` pins `crypto/envelope/v2 v2.0.0`
while another v2 module pins an earlier `crypto/envelope/v2 v2.0.0-rc.N`
transitively) fails the pre-tag gate and blocks the release.

### 1.4 Direct dependency source allowlist

Every non-rho-kit direct Go dependency must appear in
[`dependency-allowlist.txt`](dependency-allowlist.txt). The allowlist
uses exact module paths only; wildcard domains such as `github.com/*`
are intentionally unsupported. Internal modules under
`github.com/bds421/rho-kit/` are allowed implicitly because `go.work`
and local `replace` directives keep them in-tree.

CI runs:

```bash
make check-dependency-allowlist
```

That target invokes
[`tools/check-direct-dependency-allowlist.sh`](../../tools/check-direct-dependency-allowlist.sh),
which scans every tracked or newly-added `go.mod`, extracts direct
`require` entries, fails on unreviewed module paths, and also fails on
stale allowlist entries. This makes the allowlist an exact review
ledger: adding a direct dependency requires a policy diff in the same
PR, and removing a direct dependency shrinks the trusted source set.

Transitive dependencies are not listed here. They are covered by the
SBOM, `govulncheck`, `osv-scanner`, and license policy (§5, §7, §8).

### 1.5 Heavy dependency boundary guard

Some dependencies are approved sources but still must remain isolated to
adapter-specific modules. Redis, pgx, cloud-storage SDKs, KMS/Vault SDKs,
messaging SDKs, OpenFGA, Temporal, River, and Testcontainers must not
quietly move into generic modules such as `core`, `data`, `infra`, or
`httpx`.

CI runs:

```bash
make check-dependency-boundaries
```

That target invokes
[`tools/check-heavy-dependency-boundaries.sh`](../../tools/check-heavy-dependency-boundaries.sh),
which scans every tracked or newly-added `go.mod` and rejects direct
edges to those dependency clusters unless the module is the matching
adapter, the `app` composition root where explicitly allowed, or an
integration/test helper module. If a new adapter boundary is intentional,
the PR must update the gate with reviewer sign-off instead of relying on
comments in `go.mod`.

### 1.6 Verifying the policy

```bash
# Find any pseudo-versions in go.sum:
grep -r "v0.0.0-" --include=go.sum .

# Find any "latest" references (should be 0):
grep -rE "@(latest|main|master)\b" --include="*.go" --include="*.mod" .

# List Go versions across modules (should all be the same):
grep -h "^go " */go.mod */*/go.mod | sort -u

# Confirm the workspace toolchain pin (per-module go.mod files
# intentionally do not carry `toolchain`; the workspace pin is the
# single source of truth — see §1.2 above):
grep "^toolchain " go.work

# Check reviewed direct dependency sources:
make check-dependency-allowlist

# Check heavy optional SDKs stay behind adapter/test boundaries:
make check-dependency-boundaries
```

CI runs the equivalent checks in [`ci.yml`](../../.github/workflows/ci.yml)'s
`go mod download` step (which fails on missing `go.sum` entries).

---

## 2. `replace` directives and intra-repo deps

### 2.1 Why we use `replace`

The kit is a single GitHub repository (`github.com/bds421/rho-kit`)
that ships ~80 independently-versioned Go modules. Modules that
depend on each other reference the upstream import path
(`github.com/bds421/rho-kit/...`) and add a `replace` directive
that points at the local relative path:

```go
// httpx/go.mod
module github.com/bds421/rho-kit/httpx/v2

require github.com/bds421/rho-kit/core/v2 v2.0.0

replace github.com/bds421/rho-kit/core/v2 => ../core
```

Note: with v2.x.y releases, modules use Go's Semantic Import Versioning
path suffix (`/v2`). Subpackages live under the suffix
(`core/v2/secret`, not `core/secret/v2`).

`go.work` aggregates all modules so during local development and
CI, every dependency resolves to the in-tree code.

### 2.2 Why this is NOT a supply-chain risk

A casual reviewer might worry that `replace` lets the kit "escape"
its dependency declarations — that downstream consumers cloning the
kit would silently pull unreleased code. They would not, for the
following reasons:

1. **`replace` directives only apply locally.** When a downstream
   service imports `github.com/bds421/rho-kit/httpx/v2`, Go resolves
   `httpx/v2`'s declared `require` line against the *module proxy*,
   not against any path declared inside `httpx/go.mod`. The
   `replace` directive lives in the kit's repo and is invisible
   to downstream consumers — Go's module resolution intentionally
   ignores `replace` lines from indirect modules.

2. **All intra-repo modules ship via dependency-ordered tagged
   releases.** The release runbook creates one tag per `go.work` module
   in dependency levels. Dependency modules are tagged first; dependent
   modules are tidied with `GOWORK=off` and then tagged from later commits
   so their `go.sum` files can contain real internal checksums. Once a
   downstream service pulls `httpx/v2@v2.0.0`, the only `core/v2` it can
   resolve is the tagged version that `httpx/v2@v2.0.0` declared.

3. **Tagged releases on `main` are the trust anchor.** Branch
   protection on `main` requires PR review and successful CI; CI
   includes the SBOM build and the vuln scan. For v2.0.0, provenance
   is the reviewed tag commit, the GitHub release metadata, the SBOM
   workflow run, and the release-owner audit trail (§6), not a
   long-lived project signing key.

In other words: `replace` is a developer-ergonomics convenience that
lets the kit's CI run with the same code paths as a downstream
consumer would see at the latest tagged version. It does not change
the artefact a consumer receives.

### 2.3 Anti-patterns

- **Never** add a `replace` directive that points outside the
  repo (`replace foo => /home/me/forks/foo`). CI rejects PRs whose
  diff adds such lines.
- **Never** use `replace` to silently downgrade a transitive
  dependency to a vulnerable version. The govulncheck job
  (`vuln.yml`) walks the resolved module graph and treats `replace`
  targets as the canonical version.
- **Never** add a `replace` for an external module to "patch" a CVE
  without filing a PR upstream and tracking the divergence in
  `docs/audit/`. Forking is a real cost and the kit treats it as a
  last-resort.

---

## 3. Update cadence (Dependabot)

### 3.1 Configuration

Dependabot configuration is the recommended cadence policy for the
kit; `.github/dependabot.yml` is intentionally NOT shipped from the
kit repo because the kit is consumed as a Go module and downstream
services own their own dependency cadence. The table below
documents the policy each consuming service should implement when
adopting the kit. Three ecosystems are expected:

| Ecosystem | Schedule | Auto-merge | Reviewers |
|---|---|---|---|
| `gomod` (per module — Dependabot enumerates each `go.mod`) | weekly | NO — every Go dep change requires human approval | `@bds421/security` |
| `github-actions` | weekly | YES for patch and minor; manual for major | `@bds421/platform` |
| `docker` (test fixtures only — local-dev compose files) | monthly | manual | `@bds421/platform` |

For Go modules, Dependabot opens one PR per module per dep update.
This produces a high PR volume but is the only correct shape:
co-mingled bumps are hard to review and harder to revert.

### 3.2 PR vetting checklist

Every Dependabot PR must pass before merge:

- [ ] CI green: lint, test, build, **vuln, sbom**.
- [ ] The dep's release notes have been read by the reviewer (link
      is in the PR body — added by Dependabot's `include-changelog: true`).
- [ ] If the PR introduces a new direct Go dependency, the same PR
      updates `docs/audit/dependency-allowlist.txt`; CI enforces this
      via `make check-dependency-allowlist`.
- [ ] If the PR moves Redis, pgx, cloud, messaging, KMS/Vault, OpenFGA,
      Temporal, River, or Testcontainers deps into a new module, the
      module boundary is reviewed and `make check-dependency-boundaries`
      still passes.
- [ ] New transitive deps are reviewed through SBOM / vuln / license
      output; promote a transitive dep to the direct allowlist only if
      a module starts requiring it directly.
- [ ] If the dep is one of the kit's "tier-1" deps (anything in
      `crypto/`, `golang.org/x/crypto`, `golang.org/x/net`, `gopkg.in/jose`,
      `github.com/lestrrat-go/jwx`, anything below the cgo
      boundary), the diff is reviewed by `@bds421/security` even if
      Dependabot tagged it as a patch.
- [ ] The PR's CHANGELOG entry is correctly typed (`fix:` for CVE
      patches, `chore:` for non-security bumps), so the release owner
      can make the intended version impact explicit in the release notes
      and tag plan.

### 3.3 Out-of-band updates

If a CVE arrives between Dependabot runs, the security team can:

1. File the CVE as an issue with `severity: HIGH|CRITICAL` label.
2. Open a PR that bumps the affected module(s) immediately.
3. Tag the PR `security` to bypass the weekly batch.

The patch SLO (§7) starts ticking from the moment the issue is
filed, not from the next Dependabot cycle.

---

## 4. Reproducible builds

The kit's release artefacts (Go module zips published to the proxy
plus binary releases for `cmd/kit-*`) MUST be reproducible — i.e.,
two CI runs of the same release tag produce byte-identical
artefacts (modulo signature timestamps).

### 4.1 Build flags

Every binary in `cmd/kit-*` is built through `make release-bin BIN=<name>`,
which invokes `go build` with:

```bash
SOURCE_DATE_EPOCH=$(git log -1 --format=%ct) \
CGO_ENABLED=0 \
go build \
  -trimpath \                                # strip filesystem paths
  -ldflags="-s -w -buildid= \                # strip symtab, debug, build-id
            -X main.commit=$(git rev-parse HEAD) \
            -X main.date=$(git log -1 --format=%cI)" \
  -o dist/cmd/<name>/<name> \
  .
```

The flag set, in order:

- `-trimpath` rewrites embedded source paths (e.g. `/home/runner/work/...`)
  to module-relative form so panics, stack traces, and DWARF debug info
  do not diverge between build hosts.
- `-ldflags="-s -w"` strips the symbol table and DWARF debug sections.
- `-ldflags="-buildid="` zeroes Go's internal per-build ID, which would
  otherwise embed a non-deterministic salt.
- `CGO_ENABLED=0` keeps the build pure-Go. CGo embeds toolchain and
  platform paths into the binary; none of the `cmd/kit-*` binaries
  currently use CGo, so disabling it is safe and removes an entire
  class of non-determinism. If a future binary requires CGo (e.g. a
  sqlite tool), the target must be split out and the exception
  documented here in the same change.
- `SOURCE_DATE_EPOCH` is the **commit time** (`git log -1 --format=%ct`,
  Unix seconds, timezone-agnostic) of the build's HEAD. Two builds from
  the same commit produce the same `SOURCE_DATE_EPOCH` regardless of
  when or where they ran. `%cI` is used for the human-readable
  `main.date` value so it sorts lexicographically.
- `-X main.commit=$(git rev-parse HEAD)` and `-X main.date=...` pin
  build metadata when the cmd binary exposes those variables. Go
  silently ignores `-X` for symbols that are not declared, so adding
  the flags is harmless for binaries that do not expose them.

Output is written to `dist/cmd/<name>/<name>`, which is ignored by
`.gitignore`. The Make target prints the resulting `sha256` for spot
verification; the rehearse-release CI job in
[`release.yml`](../../.github/workflows/release.yml) captures a
sample build as evidence (currently `kit-doctor`).

To build every cmd binary in one shot:

```bash
make release-bin-all
```

### 4.2 Toolchain pinning

The release workflow uses `actions/setup-go@v6` with the exact
patch version of Go from `go.work` (currently `1.26.2`). Bumping
Go is a deliberate PR; it cannot happen as a side effect of a
Dependabot run because `actions/setup-go` does not auto-update.

### 4.3 Verifying reproducibility

For binary releases, the recommended verification is:

```bash
# Clone, check out a tag:
git clone https://github.com/bds421/rho-kit
cd rho-kit
git checkout cmd/kit-doctor/v2.0.0

# Build with the same flags CI uses:
make release-bin BIN=kit-doctor

# Compare against the published artefact:
sha256sum dist/cmd/kit-doctor/kit-doctor
# Expected:  <the value in the GitHub release notes>
```

To convince yourself that two independent builds of the *same source
tree* produce byte-identical artefacts (the property the §4.1 flag set
exists to deliver), the repository ships an opt-in helper:

```bash
bash tools/verify-reproducible-build.sh kit-doctor
# OK: reproducible build of kit-doctor sha256=<hash>
```

The script copies the repository into two temp directories, builds the
named binary in each, and compares hashes. The `cp -R` is intentionally
expensive across the workspace — it is a developer-side spot check
before a Go toolchain bump, an ldflags edit, or any change with
plausible determinism impact, not a per-PR gate.

The kit has not yet automated this verification (no Reproducible
Builds Project membership). The intent is to add automated binary
reproducibility checks alongside future keyless artifact
attestations.

### 4.4 Module-zip reproducibility

For Go modules (which is what 95% of consumers pull), Go's module
proxy fetches the module zip directly from GitHub at the tag. The
zip is computed by the proxy from the source tree and is
deterministic per tag — no kit-side build flags are involved. The
`go.sum` `h1:` hashes serve as the verification anchor; a
mismatched module zip is rejected by `go mod verify`.

---

## 5. SBOMs (CycloneDX)

### 5.1 What we publish

For every module-tagged release (`<module>/v<version>` pushed to
`main`), the [`sbom.yml`](../../.github/workflows/sbom.yml)
workflow generates a CycloneDX 1.5 JSON SBOM for the entire
workspace and attaches it as a release artefact named
`rho-kit.cdx.json`. The artefact contains:

- One `component` entry per direct and transitive Go module.
- `purl` of the form `pkg:golang/<module>@<version>` for each.
- The module's `h1:` hash carried in the `hashes` list.
- License metadata where the upstream module declares it.
- `dependencies` graph linking root → direct → transitive.

### 5.2 Why CycloneDX over SPDX

Documented in the workflow file's header, repeated here:

- The Anchore `syft` scanner emits richer Go-module metadata in
  CycloneDX 1.5 — `purl` + `h1:` checksums per dep.
- CycloneDX is OWASP's reference format
  (https://cyclonedx.org).
- CycloneDX 1.5 has first-class VEX (Vulnerability Exploitability
  Exchange) support, which a post-v2 follow-up can use to publish
  "we've reviewed CVE-X and it does not apply to the kit's call
  paths" attestations alongside the SBOM.

We do not publish SPDX. The format is excellent for non-Go
ecosystems, but the kit's Go-only artefact set is better served by
CycloneDX. We will revisit if a downstream consumer specifically
requires SPDX.

### 5.3 How to use a published SBOM

Downstream consumers can:

```bash
# Fetch the SBOM for a given release (substitute the actual tag,
# e.g. httpx/v2.0.0):
gh release download httpx/v2.0.0 --pattern rho-kit.cdx.json

# Run a local osv-scanner against it:
osv-scanner --sbom rho-kit.cdx.json

# Or feed it to grype:
grype sbom:rho-kit.cdx.json
```

This lets a consumer's policy gate ("does this version of `httpx`
introduce any new transitive dep with an open CRITICAL CVE?") run
without re-fetching the source tree.

### 5.4 Future: signed SBOMs

Signed SBOMs are a post-v2 follow-up. The preferred shape is keyless
Sigstore signing or GitHub artifact attestations tied to
`.github/workflows/sbom.yml`, so consumers can verify the SBOM against
the repository, workflow path, ref, and commit. Until that lands,
integrity verification relies on GitHub release HTTPS, the release
asset metadata, the workflow run, and the tagged commit.

---

## 6. Release provenance and key rotation

### 6.1 What authenticates what

| Identity or key | Purpose | Where it lives | Rotation |
|---|---|---|---|
| `release-owner` GitHub identity | Creates module tags and the coordination release from the approved release branch | GitHub account + repository audit log | rotate by changing release-owner authorization |
| `sbom-workflow` GitHub identity | Generates and uploads `rho-kit.cdx.json` for tagged releases | `.github/workflows/sbom.yml` + `GITHUB_TOKEN` scoped to the workflow run | per workflow run |
| `audit-log-hmac` | HMAC chain for `observability/auditlog` records — service-deployed, not kit-shipped | Per-deployment Vault / KMS | annually or on suspicion |
| `csrf-hmac` | Session-bound CSRF token signing | Per-deployment env var (`_FILE` mounted from secret store) | quarterly |
| `signedrequest-hmac` | Inter-service signed-request HMAC | Per-deployment KMS | quarterly |
| `paseto-keys` | PASETO local/public keypairs | Per-deployment KMS, `core/secret`-wrapped at rest in process | annually or on suspicion |
| `envelope-kek` | Envelope-encryption key-encrypting keys | Per-deployment KMS or Vault (AWS KMS / Azure Key Vault / GCP KMS / Vault Transit) | every 90d (KMS-mediated) |

The kit ships the *primitives* for every key listed above. The kit
itself does not ship or require a long-lived release-signing key for
v2.0.0.

### 6.2 Release identity for v2.0.0

The v2.0.0 release runbook intentionally separates readiness checks
from tagging and publishing:

- [`release.yml`](../../.github/workflows/release.yml) validates the
  release-candidate state but does not create tags or GitHub releases.
- [`FINAL_RELEASE_RUNBOOK_V2.md`](../release/FINAL_RELEASE_RUNBOOK_V2.md)
  is the authoritative tagging and publishing procedure.
- Module tags are created by the release owner only after the RC gates
  pass and the release owner explicitly starts the tagging phase.
- The SBOM is generated by
  [`sbom.yml`](../../.github/workflows/sbom.yml) for tag pushes and is
  attached to the matching GitHub release.

Future keyless signing or GitHub artifact attestations must update this
section, [`sbom.yml`](../../.github/workflows/sbom.yml), and the final
release runbook in the same change.

### 6.3 Rotation procedures (per-deployment keys)

For the per-deployment keys (rows 2–6 of §6.1), the kit's role is
to make rotation cheap. The expected pattern:

- **CSRF / signedrequest HMAC.** Deploy the new secret as active while
  keeping the previous secret as verification-only: CSRF uses
  `csrf.WithSecrets(current, previous...)`, inbound signed requests resolve by
  key ID, and outbound signed requests can use `sign.WrapKeyStore`. Remove the
  previous secret after the longest cookie/token/nonce overlap window.
- **PASETO key.** `crypto/paseto` accepts a `Provider` interface
  that can return multiple verification keys; the active signing
  key is one of them. Cutover: introduce new key as
  signing-and-verification, demote old key to verification-only,
  remove after the longest token TTL.
- **Envelope KEK.** `crypto/envelope` supports KEK rotation by
  storing the KEK ID in the encrypted payload header. Re-encrypt-
  on-write is the default; bulk re-encryption tools live outside
  the kit.

### 6.4 Compromise procedure

If a key listed in §6.1 is suspected compromised:

1. **Stop using the affected identity or key immediately.** For
   per-deployment keys this is a redeploy with the new secret. For
   release provenance, "compromise" means the release owner account,
   workflow token, runner, or tag-producing environment is suspect; in
   that case follow §7.5 and the rollback rules in the final release
   runbook.
2. **Mark the affected window in the audit log.** All audit-log
   entries written under the suspect key must be flagged; the HMAC
   chain catches tampering but not "the chain itself is now
   suspect".
3. **File a public advisory** on GitHub Security Advisories with
   the affected versions and the recommended consumer action.

---

## 7. Vulnerability response SLO

### 7.1 SLO targets

| Severity (CVSS / GHSA) | Time to patch | Time to release | Consumer notification |
|---|---|---|---|
| CRITICAL (9.0+) | 48 hours from disclosure to merge | 24 hours from merge to tagged release | Public advisory + GHSA watchers notified within 24h of release |
| HIGH (7.0–8.9) | 7 days from disclosure to merge | 7 days from merge to tagged release | Public advisory at release time |
| MEDIUM (4.0–6.9) | next planned release window (≤ 30 days) | ≤ 30 days | release notes |
| LOW (< 4.0) | rolled into the next Dependabot cycle | rolled into the next Dependabot cycle | release notes |

The clock starts at the *earliest* of:

- A CVE / GHSA being filed against a dep that the kit imports.
- A GitHub Private Security Advisory report (see §9).
- A `govulncheck` finding hitting the kit's `main` branch.

### 7.2 Process

1. **Triage.** Reproduce the issue against `main`. If it does not
   apply (e.g., the vulnerable dep function is not in any kit call
   path — `govulncheck` returns "not called"), the triage team
   records that exception and, once VEX publication lands, files a
   VEX statement instead of a patch.
2. **Fix.** Patch the dep version (or the kit's own code if the
   bug is ours). Add a regression test referencing the GHSA ID.
3. **Release.** Bump and tag the affected modules through the release
   runbook. The release notes include the GHSA ID and a short
   attack-vector summary.
4. **Notify.** GitHub Security Advisory publication; GHSA watchers
   and downstream consumers are notified via the advisory itself.
5. **Post-mortem** for any CRITICAL: filed under
   [`THREAT_MODEL.md`](THREAT_MODEL.md) §4 (the affected area) and
   referenced by commit or advisory ID.

### 7.3 Detection

- `vuln.yml` runs on every PR and weekly on `main`.
- `sbom.yml` produces a per-release SBOM that can be re-scanned by
  consumers at any time.
- `govulncheck` reachability mode is the canonical detector for Go
  vulns; `osv-scanner` covers manifests and GitHub Actions metadata.

### 7.4 Documented exceptions

A finding may be downgraded with an explicit, dated entry in the
PR description and a follow-up entry in
[`THREAT_MODEL.md`](THREAT_MODEL.md) §8 (gap list). Allowed reasons:

- Vulnerability is in a code path the kit does not exercise
  (govulncheck "imported but not called").
- Vulnerability is in a transitive dep used only by tests
  (`go.mod` `// indirect` and gated behind a build tag).
- Upstream patch unavailable; mitigation in place at a higher
  layer.

Each exception specifies an expiry date. CI fails if the date has
passed without the exception being renewed or cleared.

### 7.5 CI runner compromise

If the CI runner itself is suspected compromised (signs of build
tampering, unexpected artifact provenance, leaked secrets):

1. Disable the workflow that runs on the affected runner type.
2. Revoke any short-lived credentials issued to the runner.
3. Audit the last 30 days of releases for unexpected workflow runs,
   release assets, tags, attestations if enabled, and checksums.
4. Re-cut affected releases from a clean runner pool.
5. Public advisory if any release artefact's provenance cannot be
   re-verified.

---

## 8. Allowed licenses + CI verification

### 8.1 Allowed licenses

The kit ships under Apache-2.0 (see `LICENSE.md` and `NOTICE`).
It also imports open-source dependencies; allowed licenses for
direct and transitive deps:

| License | Status | Notes |
|---|---|---|
| MIT | ✅ allowed |  |
| Apache-2.0 | ✅ allowed | Preferred for new direct deps (patent grant) |
| BSD-2-Clause | ✅ allowed |  |
| BSD-3-Clause | ✅ allowed |  |
| ISC | ✅ allowed |  |
| MPL-2.0 | ⚠ case-by-case | Allowed at file level; review use to avoid linking MPL into closed-source deliverables |
| LGPL-2.1+ | ⚠ case-by-case | Static-linking concerns; review per dep |
| GPL-2.0, GPL-3.0, AGPL | ❌ forbidden | License-incompatible with the kit's licensing |
| Proprietary (any) | ❌ forbidden as transitive | Direct deps under proprietary license require an explicit kit-level decision |
| Unknown / unspecified | ❌ forbidden | Treated as proprietary until verified |

### 8.2 Enforcement

License verification is enforced by
[`tools/check-licenses.sh`](../../tools/check-licenses.sh) and the
[`.github/workflows/licenses.yml`](../../.github/workflows/licenses.yml)
workflow. The script walks every workspace module declared in `go.work`,
runs [`go-licenses`](https://github.com/google/go-licenses) (pinned to
`v1.6.0`) over each module's transitive dep graph, and fails the gate if
any declared license is not on the §8.1 allowlist.

The same checks run via `make check-licenses` locally and as a scheduled
weekly job on `main` so a quietly-bumped transitive dep cannot slip past
the allowlist between Dependabot batches. Adding a new license category
requires updating §8.1, `tools/check-licenses.sh`, and a security review
in the same PR.

### 8.3 Per-module license declarations

Each kit module is published under the same `LICENSE.md`
(Apache-2.0). The CycloneDX SBOM emitted by `sbom.yml` carries
each direct/transitive dep's license string in the `licenses` field
of the corresponding component. The repository's
[`NOTICE`](../../NOTICE) file points readers at the SBOM as the
authoritative dependency manifest and license declaration source
required by Apache 2.0 §4(d) for any deps that ship NOTICE files.

---

## 9. Security contact and private reports

### 9.1 Reporting channels

- **Preferred and only channel:** GitHub Private Security Advisory
  on `bds421/rho-kit`. Web flow:
  https://github.com/bds421/rho-kit/security/advisories/new
  This is an authenticated, end-to-end-encrypted channel with a
  durable audit trail. The project does not operate a separate
  disclosure mailbox for v2.0.0.
- **Bug bounty:** none at present; this may change post-v2.0.0.

We commit to acknowledging receipt within 24 hours and providing a
triage decision within the SLO window in §7.

### 9.2 Encrypted-report policy

The kit does not publish a long-lived project GPG key for v2.0.0, and
this document must never contain placeholder cryptographic material.
The private GitHub Security Advisory flow is the encrypted path for
initial sensitive reports. If the project later supports encrypted
email intake, the real key ID, fingerprint, algorithm, expiry, and
publication location must be added here in the same PR that publishes
the key.

### 9.3 Disclosure policy

- We follow coordinated disclosure: fix lands → public advisory →
  details published.
- The reporter is credited unless they request otherwise.
- Public details include: affected versions, CVSS score,
  reproducer, mitigation steps, and the commit that fixed it.

---

## 10. Audit trail of policy changes

This document is itself part of the supply-chain trust surface; an
attacker with commit access can downgrade the policy as easily as
they can downgrade a dep version. Mitigation:

1. **`docs/audit/SUPPLY_CHAIN.md` is owned by `@bds421/security`**
   in `CODEOWNERS`. Edits require their approval.
2. **CI runs the `vuln.yml` job on every PR**, so a downgrade of the
   `VULN_FAIL_LEVEL` value (or removal of a workflow) shows up in
   the diff and is caught by code review.
3. **Every change to this file MUST include a CHANGELOG entry**
   in the section below, with the date, author, and one-line
   description.

| Date | Author | Change |
|---|---|---|
| 2026-05 | External-auditor follow-up | Landed `tools/check-licenses.sh`, the `make check-licenses` target, and the `licenses.yml` workflow so §8.1 is CI-enforced rather than policy-only; added the repository-level `NOTICE` file and pointed §8.3 at it. |
| 2026-05 | Round-4 follow-up | Shipped the `make release-bin` / `release-bin-all` targets and `tools/verify-reproducible-build.sh`, and refined §4.1/§4.3 prose so it matches the flag set actually built (CGO_ENABLED=0, SOURCE_DATE_EPOCH=git commit-time, `-X main.commit`/`main.date`). |
| 2026-05 | Release-excellence review | Removed placeholder GPG material, clarified v2.0.0 release provenance, and aligned the policy with the actual release/SBOM workflows. |
| 2026-05 | Theme-6+ hardening | Added heavy optional SDK boundary enforcement through `make check-dependency-boundaries`. |
| 2026-05 | Theme-6+ hardening | Added exact direct Go dependency source allowlist and CI enforcement through `make check-dependency-allowlist`. |
| 2026-05 | Theme-5 author | Initial supply-chain policy document. Pinning policy, replace-directive rationale, dependabot cadence, build flags, CycloneDX SBOM, key inventory, vuln-response SLO, license allowlist, security contact. Forward-references to future signed SBOMs and license-allowlist CI. |

---

*Companion: [THREAT_MODEL.md](THREAT_MODEL.md). Completed audit artifacts live
in git history, not in package documentation.*
