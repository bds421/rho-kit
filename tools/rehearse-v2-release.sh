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
echo "==> Drop local internal replaces in rehearsal repo"
tools/drop-internal-replaces.sh
git add -A
if git diff --cached --quiet; then
  echo "No replace-removal changes to commit."
else
  git commit -q -m "rehearsal: drop internal replaces"
fi

FORBID_INTERNAL_REPLACES=1 EXPECTED_INTERNAL_VERSION="$VERSION" make check-publishable

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
go list -m all | grep -E 'github.com/bds421/rho-kit/.+/v2 v2\.0\.0'
grep -E 'github.com/bds421/rho-kit/.+ v2\.0\.0' go.sum

echo
echo "==> Verify command installs"
go install github.com/bds421/rho-kit/cmd/kit-new/v2@"$VERSION"
go install github.com/bds421/rho-kit/cmd/kit-migrate/v2@"$VERSION"
go install github.com/bds421/rho-kit/cmd/kit-doctor/v2@"$VERSION"

echo
echo "Rehearsal passed."
echo "log: $log_file"
