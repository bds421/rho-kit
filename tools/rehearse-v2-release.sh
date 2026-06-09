#!/usr/bin/env bash
set -euo pipefail

# Local-only rehearsal for the dependency-ordered v2 release.
#
# This script never pushes to the real origin. It copies the current working
# tree into a temporary git repository, creates a temporary bare origin, tags
# module levels there, then verifies Go resolution from a clean temp consumer.

VERSION="${RELEASE_VERSION:-v2.0.0}"
KEEP="${REHEARSAL_KEEP:-0}"

SOURCE_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$SOURCE_ROOT"

if [[ ! -f go.work ]]; then
  echo "ERROR: go.work not found; run from the rho-kit repository." >&2
  exit 1
fi

# The rehearsal copies the working tree wholesale via rsync below, so an
# untracked artefact (a generated binary, a forgotten profile dump, a
# half-written script) silently lands in the rehearsal repository and the
# subsequent `git add .` records it as part of the input commit. That
# poisons the rehearsal evidence: a clean re-tag from origin would not
# include those files, so the rehearsal stops being faithful. Require a
# clean tracked tree by default; let operators opt into an exploratory
# run with ALLOW_DIRTY_TREE=1 (L-170).
status="$(git status --porcelain 2>/dev/null || true)"
if [[ -n "$status" && "${ALLOW_DIRTY_TREE:-0}" != "1" ]]; then
  echo "ERROR: rehearse-v2-release: working tree is dirty; refusing to record exploratory state in canonical rehearsal evidence." >&2
  echo "Commit, stash, or clean the tree — or set ALLOW_DIRTY_TREE=1 for a local exploratory rehearsal whose log must not be checked in." >&2
  echo "Untracked or modified files:" >&2
  printf '%s\n' "$status" >&2
  exit 1
fi

timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
log_dir="$SOURCE_ROOT/docs/release/rehearsals"
mkdir -p "$log_dir"
log_file="$log_dir/${timestamp}-v2-release-rehearsal.log"

tmpdir="$(mktemp -d)"
cleanup() {
  if [[ "$KEEP" == "1" ]]; then
    echo "Keeping rehearsal temp directory: $tmpdir"
  else
    chmod -R u+w "$tmpdir" 2>/dev/null || true
    rm -rf "$tmpdir"
  fi
}
trap cleanup EXIT

exec > >(tee "$log_file") 2>&1

echo "rho-kit v2 release rehearsal"
echo "version: $VERSION"
echo "source: $SOURCE_ROOT"
echo "temp: $tmpdir"
echo "log: $log_file"
echo

repo="$tmpdir/repo"
origin="$tmpdir/origin.git"
home="$tmpdir/home"
gopath="$tmpdir/gopath"
gocache="$tmpdir/gocache"
consumer="$tmpdir/consumer"
bin_dir="$tmpdir/bin"

mkdir -p "$repo" "$home" "$gopath" "$gocache" "$consumer" "$bin_dir"

echo "==> Copy current working tree"
rsync -a --delete \
  --exclude '.git/' \
  --exclude '.claude/' \
  --exclude 'dist/' \
  --exclude 'coverage.out' \
  --exclude '.DS_Store' \
  --exclude 'docs/release/rehearsals/' \
  "$SOURCE_ROOT"/ "$repo"/

cd "$repo"
git init -q -b main
git config user.name "rho-kit release rehearsal"
git config user.email "release-rehearsal@example.invalid"
git add .
git commit -q -m "rehearsal: input workspace"

git init -q --bare "$origin"
git remote add origin "file://$origin"
remote_url="$(git remote get-url origin)"
case "$remote_url" in
  file://"$tmpdir"/*) ;;
  *)
    echo "ERROR: rehearsal origin is not inside the temp directory: $remote_url" >&2
    exit 1
    ;;
esac
git push -q origin main

export HOME="$home"
export GIT_CONFIG_GLOBAL="$home/.gitconfig"
export GOPATH="$gopath"
export GOMODCACHE="$gopath/pkg/mod"
export GOCACHE="$gocache"
export GOBIN="$bin_dir"
export GOPRIVATE='github.com/bds421/*'
export GONOPROXY='github.com/bds421/*'
export GONOSUMDB='github.com/bds421/*'
export GIT_ALLOW_PROTOCOL='file:https:ssh:git'

git config --global protocol.file.allow always
git config --global --add url."file://$origin".insteadOf "https://github.com/bds421/rho-kit"
git config --global --add url."file://$origin".insteadOf "https://github.com/bds421/rho-kit.git"
git config --global --add url."file://$origin".insteadOf "ssh://git@github.com/bds421/rho-kit"
git config --global --add url."file://$origin".insteadOf "ssh://git@github.com/bds421/rho-kit.git"
git config --global --add url."file://$origin".insteadOf "git@github.com:bds421/rho-kit"
git config --global --add url."file://$origin".insteadOf "git@github.com:bds421/rho-kit.git"

echo
echo "==> Verify no local internal replaces (one-time drop was done at v2.0.0)"
# Do NOT pass EXPECTED_INTERNAL_VERSION here. Internal requires currently
# point at the previous released version (e.g. v2.0.0 when releasing
# v2.0.1) — the per-level tidy below bumps each to the new $VERSION as
# its dependency level's tags become resolvable on origin. The final
# downstream-consumer verify confirms the resolved end state.
FORBID_INTERNAL_REPLACES=1 make check-publishable

echo
echo "==> Compute release plan"
plan="$tmpdir/release-plan.tsv"
RELEASE_MODE=all RELEASE_FORMAT=tsv RELEASE_VERSION="$VERSION" make release-plan > "$plan"
awk -F '\t' 'NR > 1 { count++; levels[$1]++ } END { print "modules=" count; for (level in levels) print "level " level "=" levels[level] }' "$plan" | sort
max_level="$(awk -F '\t' 'NR > 1 { if ($1 > max) max=$1 } END { print max+0 }' "$plan")"

for level in $(seq 0 "$max_level"); do
  echo
  echo "==> Prepare level $level"
  level_dirs="$tmpdir/level-${level}-dirs.txt"
  level_tags="$tmpdir/level-${level}-tags.txt"
  awk -F '\t' -v level="$level" 'NR > 1 && $1 == level { print $2 }' "$plan" > "$level_dirs"
  awk -F '\t' -v level="$level" 'NR > 1 && $1 == level { print $4 }' "$plan" > "$level_tags"

  while IFS= read -r dir; do
    [[ -z "$dir" ]] && continue
    echo "tidy: $dir"
    # Deterministic bump: rewrite every internal kit require line to
    # $VERSION before tidy. For v2.0.0 (first release) this is a no-op
    # because the kit checked in with requires pre-set to v2.0.0; for
    # v2.0.x where x > 0, requires currently point at the previous
    # version and need to be bumped now that previous-level tags for
    # $VERSION are on origin.
    internal_deps=$(grep -hoE 'github\.com/bds421/rho-kit/[^[:space:]]+' "$dir/go.mod" | sort -u || true)
    for dep in $internal_deps; do
      (cd "$dir" && GOWORK=off go mod edit -require="${dep}@${VERSION}") 2>/dev/null || true
    done
    (cd "$dir" && GOWORK=off go mod tidy)
  done < "$level_dirs"

  while IFS= read -r dir; do
    [[ -z "$dir" ]] && continue
    git add -A "$dir/go.mod"
    git add -A "$dir/go.sum" 2>/dev/null || true
  done < "$level_dirs"

  if git diff --cached --quiet; then
    echo "No go.mod/go.sum changes for level $level."
  else
    git commit -q -m "rehearsal: prepare ${VERSION} module level ${level}"
  fi

  commit="$(git rev-parse HEAD)"
  while IFS= read -r tag; do
    [[ -z "$tag" ]] && continue
    echo "tag: $tag -> $commit"
    git tag -a "$tag" -m "rho-kit $tag" "$commit"
  done < "$level_tags"

  git push -q --atomic origin $(cat "$level_tags")
  while IFS= read -r tag; do
    [[ -z "$tag" ]] && continue
    git ls-remote --tags https://github.com/bds421/rho-kit "refs/tags/$tag" | grep -q "refs/tags/$tag"
  done < "$level_tags"
done

echo
echo "==> Push coordination tag"
git tag -a "release/$VERSION" -m "rho-kit $VERSION release coordination tag" "$(git rev-parse HEAD)"
git push -q origin "release/$VERSION"

echo
echo "==> Verify clean downstream consumer"
# The consumer (and the command installs below) are standalone modules
# OUTSIDE the kit workspace. An inherited GOWORK (CI exports one globally)
# would make `go` try to resolve them against the kit's go.work and fail
# with "directory prefix . does not contain modules listed in go.work".
# Force module mode so the rehearsal verify is hermetic.
export GOWORK=off
cd "$consumer"
go mod init rho-kit-v2-verify
cat > main.go <<'GO'
package main

import (
	_ "github.com/bds421/rho-kit/app/v2"
	_ "github.com/bds421/rho-kit/httpx/v2"
	_ "github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2"
)

func main() {}
GO
go get \
  github.com/bds421/rho-kit/app/v2@"$VERSION" \
  github.com/bds421/rho-kit/httpx/v2@"$VERSION" \
  github.com/bds421/rho-kit/infra/messaging/amqpbackend/v2@"$VERSION"
go mod tidy
go test ./...
version_re="$(echo "$VERSION" | sed 's/\./\\./g')"
go list -m all | grep -E "github.com/bds421/rho-kit/.+/v2 ${version_re}"
grep -E "github.com/bds421/rho-kit/.+ ${version_re}" go.sum

echo
echo "==> Verify command installs"
go install github.com/bds421/rho-kit/cmd/kit-new/v2@"$VERSION"
go install github.com/bds421/rho-kit/cmd/kit-migrate/v2@"$VERSION"
go install github.com/bds421/rho-kit/cmd/kit-doctor/v2@"$VERSION"

echo
echo "Rehearsal passed."
echo "log: $log_file"
