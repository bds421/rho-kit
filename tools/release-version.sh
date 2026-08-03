#!/usr/bin/env bash
# Release a v2.0.x version of every workspace module against origin.
#
# Usage:
#   RELEASE_VERSION=v2.0.X bash tools/release-version.sh
#
# Prerequisites (run these first if not already done):
#   1. main is clean (`git status` empty) and on the commit you want
#      to tag from.
#   2. main is locally release-green (`make release-candidate` passes).
#   3. You hold the repository admin role.
#
# Branch protection no longer needs to be toggled. `main` keeps required
# CODEOWNERS reviews and linear history for everyone, but classic branch
# protection runs with `enforce_admins: false`, so the admin running this
# script can push the per-level commits and tags directly. The previous
# flow deleted `required_pull_request_reviews` before the run and restored
# it afterwards; that left `main` unprotected for the whole run and silently
# unprotected forever if the run died in the middle.
#
# Note for a future move to repository rulesets: `main`'s workflows are
# path-filtered (ci.yml has paths-ignore for docs/**, dashboards.yml and
# supply-chain.yml only trigger on their own paths). Required status checks
# therefore cannot be enabled as-is — a docs-only PR would sit forever on a
# check that never runs. Add an always-runs aggregate gate job first.
# `tools/check-release-team.sh` also reads the CLASSIC protection endpoint
# and requires `require_code_owner_reviews: true`, so it must be taught about
# rulesets before classic protection can be removed.
#
# What it does (mirrors tools/rehearse-v2-release.sh but against the
# real origin instead of a temp bare repo):
#   - Computes the dependency-ordered tag plan via `make release-plan`.
#   - For each dependency level 0..N:
#       * For each module in the level: rewrite every internal kit
#         require to RELEASE_VERSION (deterministic, no chicken-and-
#         egg), then `go mod tidy` (resolves go.sum against tags from
#         previous levels that are now on origin).
#       * Commit any go.mod/go.sum changes, push the commit.
#       * Tag every module in the level at the new HEAD, push all
#         tags atomically.
#   - Push the coordination tag `release/$RELEASE_VERSION`.
#
# Per-level push lets the next level's `go mod tidy` resolve the
# previous level's tags from origin via direct git (GONOPROXY skips
# proxy.golang.org so newly-pushed tags are immediately resolvable).
#
# The run finishes with an automated downstream-consumer smoke test: a
# throwaway module outside the workspace resolves every released module at
# $RELEASE_VERSION and builds against it. A release whose internal require
# rewrites are inconsistent fails there instead of reaching a consumer.
# Set RELEASE_SKIP_SMOKE=1 to skip it.

set -euo pipefail

VERSION="${RELEASE_VERSION:?set RELEASE_VERSION (e.g. v2.0.3) before running}"
PLAN="${RELEASE_PLAN_FILE:-/tmp/release-plan-${VERSION}.tsv}"

# Direct git resolution for kit modules so newly-pushed tags are
# immediately resolvable (skip proxy.golang.org TTL).
export GOPRIVATE='github.com/bds421/*'
export GONOPROXY='github.com/bds421/*'
export GONOSUMDB='github.com/bds421/*'

echo "==> Preflight: verify origin/main has not advanced past HEAD"
# A concurrent push to origin/main would make `git push origin main` below
# reject mid-run, after earlier levels' tags are already on origin, leaving a
# half-released state. Fail loudly up front instead.
git fetch -q origin main
if ! git merge-base --is-ancestor origin/main HEAD; then
  echo "ERROR: origin/main is not an ancestor of HEAD; fetch/rebase before releasing." >&2
  exit 1
fi

echo "==> Preflight: verify a dirty tree will not be swept into a release commit"
# The per-level loop stages `git add -A <dir>/go.mod` and `<dir>/go.sum`, so an
# unrelated edit to one of those files would be committed and tagged silently.
if ! git diff --quiet || ! git diff --cached --quiet; then
  echo "ERROR: working tree or index is dirty; commit or stash before releasing." >&2
  exit 1
fi

echo "==> Compute release plan for $VERSION"
RELEASE_MODE=all RELEASE_FORMAT=tsv RELEASE_VERSION="$VERSION" make release-plan > "$PLAN"
max_level=$(awk -F'\t' 'NR>1 && $1>max {max=$1} END{print max+0}' "$PLAN")
echo "max level: $max_level"

echo "==> Preflight: verify no $VERSION tag already exists on origin"
# `git tag -a` fails on an existing tag, and under `set -e` that aborts the run
# *after* earlier levels' tags are already pushed — the exact half-released
# state the origin/main preflight above exists to prevent. Check every planned
# tag plus the coordination tag before anything is pushed.
existing_remote_tags="$(git ls-remote --tags origin 2>/dev/null \
  | sed -n 's#.*refs/tags/\(.*\)$#\1#p' | sed 's/\^{}$//' | sort -u)"
collisions=""
while IFS= read -r tag; do
  [ -z "$tag" ] && continue
  if printf '%s\n' "$existing_remote_tags" | grep -qxF "$tag"; then
    collisions="${collisions}  $tag"$'\n'
  fi
done <<< "$(awk -F'\t' 'NR>1 {print $4}' "$PLAN"; echo "release/$VERSION")"
if [ -n "$collisions" ]; then
  echo "ERROR: $VERSION tags already exist on origin:" >&2
  printf '%s' "$collisions" >&2
  echo "       Pick an unused version, or delete those tags if the release was aborted." >&2
  exit 1
fi
echo "  no collisions"

for level in $(seq 0 "$max_level"); do
  echo ""
  echo "==> Level $level"
  level_dirs=$(awk -F'\t' -v l="$level" 'NR>1 && $1==l {print $2}' "$PLAN")
  level_tags=$(awk -F'\t' -v l="$level" 'NR>1 && $1==l {print $4}' "$PLAN")
  count=$(printf '%s\n' "$level_dirs" | grep -c . || true)
  echo "modules: $count"

  tidy_failed=0
  while IFS= read -r dir; do
    [ -z "$dir" ] && continue
    # Bump every already-required DIRECT internal kit module to
    # $VERSION before tidy. For v2.0.0 (first release) this was a
    # no-op because go.mod requires were pre-set. For v2.0.x where
    # x > 0, requires currently point at v2.0.(x-1) and need to be
    # bumped now that previous-level tags for $VERSION are on origin.
    #
    # Enumerate only DIRECT require paths by parsing the go.mod require
    # blocks. A raw `grep` over the whole file would also match the
    # `module` line, `// indirect` requires, and replace targets — none
    # of which should be -require'd here.
    internal_deps=$(
      awk '
        /^require[[:space:]]*\(/ { inreq=1; next }
        inreq && /^[[:space:]]*\)/ { inreq=0; next }
        inreq && $1 ~ /^github\.com\/bds421\/rho-kit\// && $0 !~ /\/\/[[:space:]]*indirect/ { print $1; next }
        /^require[[:space:]]+github\.com\/bds421\/rho-kit\// && $0 !~ /\/\/[[:space:]]*indirect/ { print $2 }
      ' "$dir/go.mod" | sort -u || true
    )
    for dep in $internal_deps; do
      (cd "$dir" && GOWORK=off go mod edit -require="${dep}@${VERSION}") 2>/dev/null || true
    done
    if ! (cd "$dir" && GOWORK=off go mod tidy) >/dev/null 2>&1; then
      echo "  ERROR: go mod tidy failed in $dir" >&2
      tidy_failed=1
    fi
  done <<< "$level_dirs"

  if [ "$tidy_failed" -ne 0 ]; then
    echo "ERROR: aborting release before tagging level $level — at least one" >&2
    echo "module's go mod tidy failed; its go.sum/require set would be wrong." >&2
    exit 1
  fi

  # Stage and commit if any go.mod/go.sum changed. go.sum may be absent
  # (zero-dep / internal-only modules), so stage the two files
  # independently — a missing go.sum must not make git drop the go.mod
  # bump from the release commit (combined pathspecs stage atomically).
  while IFS= read -r dir; do
    [ -z "$dir" ] && continue
    git add -A "$dir/go.mod"
    git add -A "$dir/go.sum" 2>/dev/null || true
  done <<< "$level_dirs"

  if ! git diff --cached --quiet; then
    # These mechanical commits are created only after the release candidate
    # and rehearsal have passed. Skip push-triggered GitHub workflows so one
    # coordinated release does not launch the same CI suite once per level.
    #
    # The FINAL level is the exception: it is the commit `main` is left at, and
    # skipping CI there meant the released head was never validated by CI at
    # all. Every level rewrites internal require pins, so the head can differ
    # from the last CI-verified commit in exactly the way check-tidy and
    # check-publishable exist to catch. Let the last one run.
    skip_ci=" [skip ci]"
    if [ "$level" -eq "$max_level" ]; then
      skip_ci=""
    fi
    git commit -q -m "release: prepare $VERSION module level $level$skip_ci"
    # --force-with-lease only succeeds if origin/main is still at the
    # remote-tracking ref this run last fetched; if another push raced
    # in, fail loudly here rather than silently overwriting it. (The
    # commits are fast-forward over HEAD's ancestor anyway; the lease is
    # purely a concurrent-writer guard.)
    git push --force-with-lease=refs/heads/main:"$(git rev-parse origin/main)" origin main
  fi

  commit=$(git rev-parse HEAD)
  while IFS= read -r tag; do
    [ -z "$tag" ] && continue
    git tag -a "$tag" -m "rho-kit $tag" "$commit"
  done <<< "$level_tags"

  # `grep .` exits 1 on an empty level, which would abort the whole run
  # under set -e (after earlier levels' tags are already on origin); guard
  # with `|| true`. An empty tag list is then skipped explicitly.
  tags_args=$(printf '%s\n' "$level_tags" | grep . | tr '\n' ' ' || true)
  if [ -n "$tags_args" ]; then
    git push --atomic origin $tags_args
    echo "  pushed $count tags at $commit"
  else
    echo "  no tags for level $level"
  fi
done

echo ""
echo "==> Coordination tag"
git tag -a "release/$VERSION" -m "rho-kit $VERSION release coordination tag" HEAD
git push origin "release/$VERSION"

if [ "${RELEASE_SKIP_SMOKE:-0}" = "1" ]; then
  echo ""
  echo "==> Downstream smoke test SKIPPED (RELEASE_SKIP_SMOKE=1)"
else
  echo ""
  echo "==> Downstream consumer smoke test"
  # Resolve every released module from a throwaway module OUTSIDE the
  # workspace, so go.work cannot mask a bad require pin with local sources.
  # This is the check that actually proves the release is consumable.
  smoke_dir="$(mktemp -d)"
  trap 'rm -rf "$smoke_dir"' EXIT

  # macOS ships bash 3.2, which has no `mapfile`; keep this readable there.
  awk -F'\t' 'NR>1 {print $3}' "$PLAN" | grep . | sort -u > "$smoke_dir/modules.txt"
  smoke_count="$(grep -c . "$smoke_dir/modules.txt" || true)"
  if [ "${smoke_count:-0}" -eq 0 ]; then
    echo "ERROR: release plan carried no module paths to smoke test." >&2
    exit 1
  fi

  (
    cd "$smoke_dir"
    GOWORK=off go mod init rho-kit-release-smoke >/dev/null
    sed "s|\$|@$VERSION|" modules.txt \
      | xargs env GOWORK=off GOFLAGS=-mod=mod go get
  ) || {
    echo "ERROR: downstream smoke test failed to resolve $VERSION." >&2
    echo "       The tags are already pushed; investigate before announcing." >&2
    exit 1
  }

  skew="$(cd "$smoke_dir" && GOWORK=off go list -m all 2>/dev/null \
    | awk '/github.com\/bds421\/rho-kit/ {print $2}' | sort -u | grep -v "^$VERSION$" || true)"
  if [ -n "$skew" ]; then
    echo "ERROR: resolved rho-kit modules at unexpected versions:" >&2
    printf '  %s\n' $skew >&2
    exit 1
  fi
  echo "  $smoke_count modules resolve at $VERSION with no version skew"
fi

echo ""
echo "Release complete. Remember to:"
echo "  - Create a GitHub Release: gh release create release/$VERSION --notes ..."
echo "  - Check CI on the final release commit (the last level intentionally runs it)."
