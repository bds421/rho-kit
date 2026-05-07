# rho-kit supply-chain policy — v2.0.0

> **Status:** living document. Companion to
> [THREAT_MODEL.md](THREAT_MODEL.md). The threat model covers
> attacks on a *running* service that uses the kit; this document
> covers attacks on the *path the kit's code takes* from source to
> running service — dependencies, signing, build reproducibility,
> CVE response, and provenance.

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
6. [Signing keys and rotation](#6-signing-keys-and-rotation)
7. [Vulnerability response SLO](#7-vulnerability-response-slo)
8. [Allowed licenses + CI verification](#8-allowed-licenses--ci-verification)
9. [Security contact and encrypted reports](#9-security-contact-and-encrypted-reports)
10. [Audit trail of policy changes](#10-audit-trail-of-policy-changes)

---

## 1. Dependency-pinning policy

### 1.1 Required form for every Go module

Every `go.mod` in the workspace MUST satisfy all of:

- `go` directive pinned to an exact patch version (e.g.
  `go 1.26.2`, never `go 1.26`).
- `toolchain` directive pinned to an exact patch version
  (`toolchain go1.26.2`).
- Every `require` line uses an exact semver tag — never `v0.0.0-`
  pseudo-versions for external code, never `latest`, never a
  branch reference.
- Every `require` line for an intra-repo module is paired with a
  `replace` directive (see §2).
- Every entry in `go.sum` is preserved; deletions only happen via
  `go mod tidy` after a deliberate version change.

The CI pipeline (`.github/workflows/ci.yml`) runs `npx nx affected
-t build` which transitively executes `go build` per module; a
missing checksum or floating version surfaces as a build failure.

### 1.2 Why exact tags

Pseudo-versions (`v0.0.0-20260101...-abc123def`) are tempting for
"just one fix not in a tagged release". They are forbidden because:

- They bypass the upstream maintainer's release gate (no tag, no
  intent to release).
- They bypass `govulncheck`'s tag-based affected-version matching
  (see [vuln.yml](../../.github/workflows/vuln.yml)).
- They make SBOM diffs noisy — every CI run produces a slightly
  different `purl` if the pseudo-version updates.

Exception: the kit's *own* CI pre-tagging step inside the release
workflow temporarily produces pseudo-versions while NX Release
computes the next tag. Those versions never reach `main`.

### 1.3 Module-graph constraint

The kit ships ~80 Go modules sharing a single `go.work`. Every
module that depends on `crypto/passhash` (for example) MUST
reference the same version of it. This is enforced by NX Release's
`updateDependents` mechanism (see `nx.json`); when one module's
public API changes, NX bumps the version of every dependent and
regenerates `go.sum` files in the same release commit.

A divergent version pin (e.g. `app` pins `crypto/envelope v1.2.0`
while `crypto/paseto` pins `crypto/envelope v1.1.0` transitively)
produces an NX-Release validation error and blocks the release.

### 1.4 Verifying the policy

```bash
# Find any pseudo-versions in go.sum:
grep -r "v0.0.0-" --include=go.sum .

# Find any "latest" references (should be 0):
grep -rE "@(latest|main|master)\b" --include="*.go" --include="*.mod" .

# List Go versions across modules (should all be the same):
grep -h "^go " */go.mod */*/go.mod | sort -u

# List toolchain versions across modules (same):
grep -h "^toolchain " */go.mod */*/go.mod | sort -u
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
module github.com/bds421/rho-kit/httpx

require github.com/bds421/rho-kit/core/secret v0.5.0

replace github.com/bds421/rho-kit/core/secret => ../core/secret
```

`go.work` aggregates all modules so during local development and
CI, every dependency resolves to the in-tree code.

### 2.2 Why this is NOT a supply-chain risk

A casual reviewer might worry that `replace` lets the kit "escape"
its dependency declarations — that downstream consumers cloning the
kit would silently pull unreleased code. They would not, for the
following reasons:

1. **`replace` directives only apply locally.** When a downstream
   service imports `github.com/bds421/rho-kit/httpx`, Go resolves
   `httpx`'s declared `require` line against the *module proxy*,
   not against any path declared inside `httpx/go.mod`. The
   `replace` directive lives in the kit's repo and is invisible
   to downstream consumers — Go's module resolution intentionally
   ignores `replace` lines from indirect modules.

2. **All intra-repo modules ship via tagged releases on merge to
   main.** NX Release bumps versions, regenerates `go.sum`, and
   pushes one tag per module per release commit. Once a downstream
   service pulls `httpx@v1.6.0`, the only `core/secret` it can
   resolve is the tagged version that `httpx@v1.6.0` declared.

3. **Tagged releases on `main` are the trust anchor.** Branch
   protection on `main` requires PR review and successful CI; CI
   includes the SBOM build and the vuln scan. The signing keys
   (§6) attest the tag itself, not the working tree.

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

The kit ships a Dependabot config (`.github/dependabot.yml`,
landed alongside this document or as the next supply-chain
follow-up) with four ecosystems:

| Ecosystem | Schedule | Auto-merge | Reviewers |
|---|---|---|---|
| `gomod` (per module — Dependabot enumerates each `go.mod`) | weekly | NO — every Go dep change requires human approval | `@bds421/security` |
| `github-actions` | weekly | YES for patch and minor; manual for major | `@bds421/platform` |
| `npm` (NX toolchain at repo root) | weekly | YES for patch; manual for minor / major | `@bds421/platform` |
| `docker` (test fixtures only — local-dev compose files) | monthly | manual | `@bds421/platform` |

For Go modules, Dependabot opens one PR per module per dep update.
This produces a high PR volume but is the only correct shape:
co-mingled bumps are hard to review and harder to revert.

### 3.2 PR vetting checklist

Every Dependabot PR must pass before merge:

- [ ] CI green: lint, test, build, **vuln, sbom**.
- [ ] The dep's release notes have been read by the reviewer (link
      is in the PR body — added by Dependabot's `include-changelog: true`).
- [ ] If the dep introduces a new transitive dep, that dep is on
      the allowed list (§8) — checked manually until the
      allowed-list CI rule lands (see THREAT_MODEL §8 GAP-10).
- [ ] If the dep is one of the kit's "tier-1" deps (anything in
      `crypto/`, `golang.org/x/crypto`, `golang.org/x/net`, `gopkg.in/jose`,
      `github.com/lestrrat-go/jwx`, anything below the cgo
      boundary), the diff is reviewed by `@bds421/security` even if
      Dependabot tagged it as a patch.
- [ ] The PR's CHANGELOG entry is correctly typed (`fix:` for CVE
      patches, `chore:` for non-security bumps) — NX Release
      derives the next version bump from this prefix.

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

Every binary in `cmd/kit-*` is built with:

```bash
go build \
  -trimpath \                              # strip filesystem paths
  -ldflags="-s -w -buildid= \              # strip symtab, debug, build-id
            -X main.Version=$VERSION \     # injected by NX Release
            -X main.Commit=$GITHUB_SHA \   # injected by CI
            -X main.BuildDate=$SOURCE_DATE_EPOCH"
```

`-trimpath` ensures `/home/runner/work/...` paths are not embedded
in panics or stack traces — those would diverge between runners.
`-buildid=` removes Go's internal build ID, which embeds a
non-deterministic salt by default.

`SOURCE_DATE_EPOCH` is set from the tagged commit's author date so
two builds of the same tag produce the same `BuildDate` string.

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
git checkout cmd/kit-doctor/v0.3.0

# Build with the same flags CI uses:
make release-bin BIN=kit-doctor

# Compare against the published artefact:
sha256sum dist/kit-doctor-linux-amd64
# Expected:  <the value in the GitHub release notes>
```

The kit has not yet automated this verification (no Reproducible
Builds Project membership). The intent is to do so in Theme 5.1
alongside Sigstore signing.

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
  Exchange) support, which we will use in Theme 5.1 to publish
  "we've reviewed CVE-X and it does not apply to the kit's call
  paths" attestations alongside the SBOM.

We do not publish SPDX. The format is excellent for non-Go
ecosystems, but the kit's Go-only artefact set is better served by
CycloneDX. We will revisit if a downstream consumer specifically
requires SPDX.

### 5.3 How to use a published SBOM

Downstream consumers can:

```bash
# Fetch the SBOM for a given release:
gh release download httpx/v1.6.0 --pattern rho-kit.cdx.json

# Run a local osv-scanner against it:
osv-scanner --sbom rho-kit.cdx.json

# Or feed it to grype:
grype sbom:rho-kit.cdx.json
```

This lets a consumer's policy gate ("does this version of `httpx`
introduce any new transitive dep with an open CRITICAL CVE?") run
without re-fetching the source tree.

### 5.4 Future: signed SBOMs

Theme 5.1 will sign the SBOM with the kit's release-signing key
(§6) so the consumer can verify the artefact's provenance. Until
that lands, integrity verification relies on GitHub's release
HTTPS + the workflow's commit attestation.

---

## 6. Signing keys and rotation

### 6.1 What signs what

| Key | Purpose | Where it lives | Rotation |
|---|---|---|---|
| `release-signing` (Sigstore-issued, ephemeral) | Signs git tags during `nx release publish` | GitHub OIDC → Sigstore Fulcio (no long-term key material) | per-release (ephemeral) |
| `audit-log-hmac` | HMAC chain for `observability/auditlog` records — service-deployed, not kit-shipped | Per-deployment Vault / KMS | annually or on suspicion |
| `csrf-hmac` | Session-bound CSRF token signing | Per-deployment env var (`_FILE` mounted from secret store) | quarterly |
| `signedrequest-hmac` | Inter-service signed-request HMAC | Per-deployment KMS | quarterly |
| `paseto-keys` | PASETO local/public keypairs | Per-deployment KMS, `core/secret`-wrapped at rest in process | annually or on suspicion |
| `envelope-kek` | Envelope-encryption key-encrypting keys | Per-deployment KMS (AWS KMS / GCP KMS / Azure Key Vault) | every 90d (KMS-mediated) |

The kit ships the *primitives* for every key listed above. The kit
itself only needs the Sigstore release-signing key.

### 6.2 Who can use the release key

The release signing flow runs entirely inside the
[`release.yml`](../../.github/workflows/release.yml) workflow on
GitHub-hosted runners. The flow uses GitHub's OIDC token to obtain
a short-lived Sigstore certificate; no long-term private key is
ever stored. As a consequence:

- A user with write access to `main` can trigger a release
  (workflow_dispatch).
- The CI runner produces signatures that include the GitHub
  identity in the certificate; downstream verification can pin to
  this identity (`bds421/rho-kit / .github/workflows/release.yml`).
- There is nothing to rotate in the conventional sense — every
  release uses a fresh ephemeral certificate.

### 6.3 Rotation procedures (per-deployment keys)

For the per-deployment keys (rows 2–6 of §6.1), the kit's role is
to make rotation cheap. The expected pattern:

- **CSRF / signedrequest HMAC.** Wrap the new secret in
  `core/secret.String`, redeploy the service, observe one
  request-cycle's worth of "old token rejected" errors during the
  cutover, then remove the old secret. The kit's middleware
  supports a comma-separated list of accepted secrets so the cutover
  can be zero-downtime.
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

1. **Stop using the key for new signatures immediately.** For
   per-deployment keys this is a redeploy with the new secret. For
   the release key it's a `gh release delete` + re-cut from a
   different runner — though since the release key is ephemeral
   per-build, "compromise" here means the runner itself is
   compromised, in which case we follow §7.5.
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
| CRITICAL (9.0+) | 48 hours from disclosure to merge | 24 hours from merge to tagged release | Public advisory + email to security@ subscribers within 24h of release |
| HIGH (7.0–8.9) | 7 days from disclosure to merge | 7 days from merge to tagged release | Public advisory at release time |
| MEDIUM (4.0–6.9) | next planned release window (≤ 30 days) | ≤ 30 days | release notes |
| LOW (< 4.0) | rolled into the next Dependabot cycle | rolled into the next Dependabot cycle | release notes |

The clock starts at the *earliest* of:

- A CVE / GHSA being filed against a dep that the kit imports.
- A private report to security@bds421.example (see §9).
- A `govulncheck` finding hitting the kit's `main` branch.

### 7.2 Process

1. **Triage.** Reproduce the issue against `main`. If it does not
   apply (e.g., the vulnerable dep function is not in any kit call
   path — `govulncheck` returns "not called"), the triage team
   files a VEX statement instead of a patch (Theme 5.1).
2. **Fix.** Patch the dep version (or the kit's own code if the
   bug is ours). Add a regression test referencing the GHSA ID.
3. **Release.** Bump the affected modules via NX Release. The
   release notes include the GHSA ID and a short attack-vector
   summary.
4. **Notify.** GitHub Security Advisory + email to security@
   subscribers.
5. **Post-mortem** for any CRITICAL: filed under
   [`THREAT_MODEL.md`](THREAT_MODEL.md) §4 (the affected area) and
   linked from [`CRITICAL.md`](CRITICAL.md).

### 7.3 Detection

- `vuln.yml` runs on every PR and weekly on `main`.
- `sbom.yml` produces a per-release SBOM that can be re-scanned by
  consumers at any time.
- `govulncheck` reachability mode is the canonical detector for Go
  vulns; `osv-scanner` for non-Go ecosystems (npm, GitHub Actions).

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
tampering, unexpected Sigstore certificates, leaked secrets):

1. Disable the workflow that runs on the affected runner type.
2. Revoke any short-lived credentials issued to the runner.
3. Audit the last 30 days of releases for unexpected signatures.
4. Re-cut affected releases from a clean runner pool.
5. Public advisory if any release artefact's provenance cannot be
   re-verified.

---

## 8. Allowed licenses + CI verification

### 8.1 Allowed licenses

The kit's deliverable is itself proprietary (see `LICENSE.md`), but
it imports open-source dependencies. Allowed licenses for direct
and transitive deps:

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

### 8.2 Enforcement (planned)

Today, license verification is a manual review step on Dependabot
PRs (the PR body shows the dep's license). The intent is to land a
CI rule (Theme 5.1) that:

- Walks the workspace's transitive dep graph (`go list -m
  -mod=mod -json all`).
- Cross-references each dep's license against the allowed list.
- Fails the PR if a dep's license is forbidden or unknown.

The implementation will likely use
[`fossa-cli`](https://fossa.com) or
[`go-licenses`](https://github.com/google/go-licenses) — selection
is open. Until it lands, this section is policy without
automation; treat it accordingly when reviewing Dependabot PRs.

### 8.3 Per-module license declarations

Each kit module is published under the same `LICENSE.md` (the kit
is proprietary). The CycloneDX SBOM emitted by `sbom.yml` carries
each direct/transitive dep's license string in the `licenses` field
of the corresponding component.

---

## 9. Security contact and encrypted reports

### 9.1 Reporting channels

- **Preferred:** GitHub Security Advisory (private vulnerability
  report) on `bds421/rho-kit`. Web flow:
  https://github.com/bds421/rho-kit/security/advisories/new
- **Email:** security@bds421.example (responses within 1 business
  day; reports require GPG encryption — see §9.2).
- **Bug bounty:** none at present; this may change post-v2.0.0.

We commit to acknowledging receipt within 24 hours and providing a
triage decision within the SLO window in §7.

### 9.2 GPG key for encrypted reports

```
Key ID:       0xDEADBEEFCAFEBABE                  (placeholder —
                                                   the real key is
                                                   published to the
                                                   org's keys.openpgp.org
                                                   profile)
Fingerprint:  ABCD EF12 3456 7890 ABCD  EF12 3456 7890 DEAD BEEF
Algorithm:    RSA 4096
Expiry:       2027-05
```

The fingerprint above is a placeholder pending the security-team
key generation ceremony scheduled for the v2.0.0 release. The
authoritative fingerprint will be published in the GitHub Security
tab and on the project README before v2.0.0 ships.

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
| 2026-05 | Theme-5 author | Initial supply-chain policy document. Pinning policy, replace-directive rationale, dependabot cadence, build flags, CycloneDX SBOM, key inventory, vuln-response SLO, license allowlist, security contact. Forward-references to Theme 5.1 (Sigstore signing, license-allowlist CI rule). |

---

*Companion: [THREAT_MODEL.md](THREAT_MODEL.md). Per-finding ledger:
[CRITICAL.md](CRITICAL.md). Roadmap of remaining audit items:
[ROADMAP.md](ROADMAP.md).*
