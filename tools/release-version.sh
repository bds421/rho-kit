#!/usr/bin/env bash
# Release a v2.0.x version of every workspace module against origin.
#
# Usage:
#   RELEASE_VERSION=v2.0.X bash tools/release-version.sh
#
# Prerequisites (run these first if not already done):
#   1. main is clean (`git status` empty) and on the commit you want
#      to tag from.
#   2. main is CI-green (`make ci` passes locally; CI on the latest
#      push is also success).
#   3. Branch protection's required PR review is temporarily disabled
#      (the dance pushes ~7 commits + tag batches directly to main).
#      Re-enable it after the run.
#
#      gh api -X DELETE repos/<owner>/<repo>/branches/main/protection/required_pull_request_reviews
#      ... run this script ...
#      gh api -X PATCH ... (restore)
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
# After the script completes, run a downstream-consumer smoke test:
#   tmpdir=$(mktemp -d); cd "$tmpdir"
#   go mod init verify
#   go get github.com/bds421/rho-kit/app/v2@$RELEASE_VERSION ...
#   go list -m all | grep rho-kit  # should all show $RELEASE_VERSION

set -euo pipefail

VERSION="${RELEASE_VERSION:?set RELEASE_VERSION (e.g. v2.0.3) before running}"
PLAN="${RELEASE_PLAN_FILE:-/tmp/release-plan-${VERSION}.tsv}"

# Direct git resolution for kit modules so newly-pushed tags are
# immediately resolvable (skip proxy.golang.org TTL).
export GOPRIVATE='github.com/bds421/*'
export GONOPROXY='github.com/bds421/*'
export GONOSUMDB='github.com/bds421/*'

echo "==> Compute release plan for $VERSION"
RELEASE_MODE=all RELEASE_FORMAT=tsv RELEASE_VERSION="$VERSION" make release-plan > "$PLAN"
max_level=$(awk -F'\t' 'NR>1 && $1>max {max=$1} END{print max+0}' "$PLAN")
echo "max level: $max_level"

for level in $(seq 0 "$max_level"); do
  echo ""
  echo "==> Level $level"
  level_dirs=$(awk -F'\t' -v l="$level" 'NR>1 && $1==l {print $2}' "$PLAN")
  level_tags=$(awk -F'\t' -v l="$level" 'NR>1 && $1==l {print $4}' "$PLAN")
  count=$(echo "$level_dirs" | grep -c .)
  echo "modules: $count"

  while IFS= read -r dir; do
    [ -z "$dir" ] && continue
    # Bump every already-required internal kit module to $VERSION
    # before tidy. For v2.0.0 (first release) this was a no-op
    # because go.mod requires were pre-set. For v2.0.x where x > 0,
    # requires currently point at v2.0.(x-1) and need to be bumped
    # now that previous-level tags for $VERSION are on origin.
    internal_deps=$(grep -hoE 'github\.com/bds421/rho-kit/[^[:space:]]+' "$dir/go.mod" | sort -u || true)
    for dep in $internal_deps; do
      (cd "$dir" && GOWORK=off go mod edit -require="${dep}@${VERSION}") 2>/dev/null || true
    done
    (cd "$dir" && GOWORK=off go mod tidy) >/dev/null 2>&1 || echo "  (tidy issue in $dir)"
  done <<< "$level_dirs"

  # Stage and commit if any go.mod/go.sum changed
  while IFS= read -r dir; do
    [ -z "$dir" ] && continue
    git add -A "$dir/go.mod" "$dir/go.sum" 2>/dev/null || true
  done <<< "$level_dirs"

  if ! git diff --cached --quiet; then
    git commit -q -m "release: prepare $VERSION module level $level"
    git push origin main
  fi

  commit=$(git rev-parse HEAD)
  while IFS= read -r tag; do
    [ -z "$tag" ] && continue
    git tag -a "$tag" -m "rho-kit $tag" "$commit"
  done <<< "$level_tags"

  tags_args=$(echo "$level_tags" | grep . | tr '\n' ' ')
  git push --atomic origin $tags_args
  echo "  pushed $count tags at $commit"
done

echo ""
echo "==> Coordination tag"
git tag -a "release/$VERSION" -m "rho-kit $VERSION release coordination tag" HEAD
git push origin "release/$VERSION"

echo ""
echo "Release complete. Remember to:"
echo "  - Re-enable branch protection required PR review."
echo "  - Run a downstream consumer smoke test (go get ...@$VERSION)."
echo "  - Optionally create a GitHub Release: gh release create release/$VERSION --notes ..."
