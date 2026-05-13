# rho-kit v2 Dependency-Ordered Tagging Plan

This document is the future tagging plan for v2.0.0. It is not permission to
tag or publish during release preparation.

Snippet status: shell blocks are executable from the repository root when the
release owner deliberately enters the future tagging phase. Do not run the
replace-dropping, tidy, tag-creation, push, or GitHub-release blocks during
release preparation.

## Strategy

rho-kit is a multi-module repository. There is no root `go.mod`, so a single
root tag named `v2.0.0` is not sufficient for Go module resolution.

Each workspace module must get a Go subdirectory tag:

```text
<module-directory>/v2.0.0
```

Examples:

```text
core/v2.0.0
httpx/v2.0.0
infra/messaging/amqpbackend/v2.0.0
app/v2.0.0
examples/agentic-service/v2.0.0
```

Module tags do not all have to point at the same commit. For this repo they
should be created in dependency order so each dependent module can commit
`go.sum` entries for already-tagged internal dependencies.

The human-facing GitHub release object should use one extra coordination tag:

```text
release/v2.0.0
```

That coordination tag is not the Go module release signal; the module-prefixed
tags are.

## Compute The Plan

Use the repo-native planner. It reads `go.work`, each module's `go.mod`, and
the internal `require` graph.

Full v2 release plan:

```bash
RELEASE_MODE=all make release-plan
```

Machine-readable tag plan:

```bash
RELEASE_MODE=all RELEASE_FORMAT=tags make release-plan | tee /tmp/rho-kit-v2-tags-by-level.txt
```

As of this release-prep package, the full plan contains 67 module tags across
five dependency levels. Modules in the same level do not depend on each other
and can be tagged together after that level's `go.mod`/`go.sum` files are
tidied and committed.

To inspect what changed since a base ref and which dependents are impacted:

```bash
RELEASE_BASE_REF=HEAD~1 make release-plan
RELEASE_BASE_REF=origin/main make release-plan
```

Root-level changes are reported separately. They are not treated as module
changes unless the release owner opts into a conservative all-modules impact:

```bash
RELEASE_BASE_REF=origin/main RELEASE_GLOBAL_CHANGES=all make release-plan
```

## Rehearse Locally First

Before using any real remote, run the local-only rehearsal:

```bash
tools/rehearse-v2-release.sh
```

The rehearsal copies the current working tree into a temporary repository,
creates a temporary bare `origin`, drops internal replaces there, tags each
dependency level against the temp origin, and verifies a clean downstream
consumer can resolve selected modules at `v2.0.0` with real internal
`go.sum` checksums.

It writes a log under:

```text
docs/release/rehearsals/
```

Set `REHEARSAL_KEEP=1` to keep the temporary repository for debugging:

```bash
REHEARSAL_KEEP=1 tools/rehearse-v2-release.sh
```

## Workspace Dependency Invariant

Local `replace` directives are useful while developing, but they prevent
repository `go.sum` files from recording checksums for internal module tags.
Before the dependency-ordered release branch starts, drop internal replaces and
use `go.work` for local development:

```bash
tools/drop-internal-replaces.sh
FORBID_INTERNAL_REPLACES=1 make check-publishable
```

Consumers ignore replace directives from dependencies, but release artifacts
should still avoid publishing wrong or unverifiable checksum state. The
release-critical invariant is:

```bash
FORBID_INTERNAL_REPLACES=1 EXPECTED_INTERNAL_VERSION=v2.0.0 make check-publishable
```

This fails if:

- any internal module is still pinned at `v0.0.0`;
- any internal `require` points at a version other than `v2.0.0`;
- any local internal `replace` remains;
- any module's `go` directive differs from `go.work`.

## Pre-Release Checks

Do not create any tag unless all of these checks pass on the exact release
branch state:

```bash
git status --short
git diff --check
make test
make lint
make check-dependency-boundaries
make check-dependency-allowlist
make check-operational-readiness
FORBID_INTERNAL_REPLACES=1 EXPECTED_INTERNAL_VERSION=v2.0.0 make check-publishable
GOCACHE=/private/tmp/rho-kit-gocache go run ./cmd/kit-doctor -format=json -strict=critical .
make vulncheck
make test-race
make test-integration
RELEASE_MODE=all make release-plan
```

Expected output summary:

- `git status --short` prints nothing.
- `git diff --check` prints nothing.
- `make check-dependency-boundaries` prints an OK line; current evidence is
  `348 direct module edges checked`.
- `make check-dependency-allowlist` prints an OK line; current evidence is
  `59 direct external deps approved`.
- `make check-operational-readiness` prints `operational readiness check OK
  (67 modules covered)`.
- `FORBID_INTERNAL_REPLACES=1 EXPECTED_INTERNAL_VERSION=v2.0.0 make
  check-publishable` confirms no internal replaces remain and all internal
  requires point at `v2.0.0`.
- `cmd/kit-doctor -strict=critical` prints `null`.
- The test, lint, vulncheck, race, and integration gates contain no `FAIL`.
- `RELEASE_MODE=all make release-plan` prints the dependency levels.

## Release Level Loop Later

Run this only after the release owner explicitly starts tagging.

First, create the dependency plan:

```bash
VERSION=v2.0.0
RELEASE_MODE=all RELEASE_FORMAT=tsv make release-plan > /tmp/rho-kit-v2-plan.tsv
```

For each level, starting at level 0:

1. Ensure all tags from lower levels are pushed and visible on `origin`.
2. Run `GOWORK=off go mod tidy` for every module in the current level.
3. Commit the `go.mod` / `go.sum` changes for that level, if any.
4. Tag every module in the level at that commit.
5. Push that level's tags atomically.

Current-level tidy command shape:

```bash
LEVEL=0
awk -F '\t' -v level="$LEVEL" 'NR > 1 && $1 == level { print $2 }' /tmp/rho-kit-v2-plan.tsv |
while IFS= read -r dir; do
  echo "==> tidy $dir"
  (cd "$dir" && GOWORK=off go mod tidy)
done
```

Commit the level after reviewing the diff:

```bash
LEVEL=0
git status --short
git diff --check
awk -F '\t' -v level="$LEVEL" 'NR > 1 && $1 == level { print $2 }' /tmp/rho-kit-v2-plan.tsv |
while IFS= read -r dir; do
  git add "$dir/go.mod"
  if [ -f "$dir/go.sum" ]; then
    git add "$dir/go.sum"
  fi
done
git commit -m "chore(release): prepare v2.0.0 module level ${LEVEL}"
```

If the level produces no file changes, do not create an empty commit; tag the
current commit.

Create and push the level's tags:

```bash
LEVEL=0
COMMIT="$(git rev-parse HEAD)"
awk -F '\t' -v level="$LEVEL" 'NR > 1 && $1 == level { print $4 }' /tmp/rho-kit-v2-plan.tsv > /tmp/rho-kit-v2-level-tags.txt

while IFS= read -r tag; do
  git tag -a "$tag" -m "rho-kit $tag" "$COMMIT"
done < /tmp/rho-kit-v2-level-tags.txt

git push --dry-run --atomic origin $(cat /tmp/rho-kit-v2-level-tags.txt)
git push --atomic origin $(cat /tmp/rho-kit-v2-level-tags.txt)
```

Repeat for every level in `/tmp/rho-kit-v2-plan.tsv`.

After the last module level is pushed, create the coordination tag:

```bash
VERSION=v2.0.0
git tag -a "release/$VERSION" \
  -m "rho-kit $VERSION release coordination tag" \
  "$(git rev-parse HEAD)"
git push --dry-run origin "release/$VERSION"
git push origin "release/$VERSION"
```

Do not use `git push --tags`; it can publish unrelated local tags.

## Verify Tags From A Clean Consumer Later

After the dependency-ordered tag push, verify resolution from outside the
repository:

```bash
set -euo pipefail

VERSION=v2.0.0
tmpdir="$(mktemp -d)"
cd "$tmpdir"
go mod init rho-kit-v2-verify

GOPRIVATE=github.com/bds421/* \
GONOSUMDB=github.com/bds421/* \
go get \
  github.com/bds421/rho-kit/app/v2@${VERSION} \
  github.com/bds421/rho-kit/httpx/v2@${VERSION} \
  github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2@${VERSION}

cat > main.go <<'EOF'
package main

import (
	_ "github.com/bds421/rho-kit/app/v2"
	_ "github.com/bds421/rho-kit/httpx/v2"
	_ "github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2"
)

func main() {}
EOF

go mod tidy
go list -deps ./... >/dev/null
go list -m all | rg 'github.com/bds421/rho-kit/.+/v2 v2\.0\.0'
rg 'github.com/bds421/rho-kit/.+ v2\.0\.0' go.sum
```

Expected output: the selected modules and their internal rho-kit dependencies
resolve at `v2.0.0`, and the clean consumer `go.sum` contains internal module
checksums.

## GitHub Release Later

After the clean-consumer verification passes, create a GitHub draft release
attached to the coordination tag:

```bash
VERSION=v2.0.0
gh release create "release/$VERSION" \
  --repo bds421/rho-kit \
  --title "rho-kit $VERSION" \
  --notes-file docs/RELEASE_NOTES_v2.md \
  --draft
```

Review the draft in GitHub, then publish it manually or with:

```bash
VERSION=v2.0.0
gh release edit "release/$VERSION" --repo bds421/rho-kit --draft=false
```

## Rollback

Before pushing a level's tags, rollback is local:

```bash
git tag -d $(cat /tmp/rho-kit-v2-level-tags.txt)
```

After pushing tags, rollback is a release-owner decision because consumers may
already have resolved versions. If the push was accidental and no consumers
have adopted it, delete the remote tags and then delete local tags:

```bash
git push --delete origin $(cat /tmp/rho-kit-v2-level-tags.txt)
git tag -d $(cat /tmp/rho-kit-v2-level-tags.txt)
```

If a draft GitHub release was created by mistake, delete only the draft release
first:

```bash
VERSION=v2.0.0
gh release delete "release/$VERSION" --repo bds421/rho-kit --yes
```

Do not rewrite or delete public module tags after downstream users may have
resolved them. Cut a follow-up patch release instead.
