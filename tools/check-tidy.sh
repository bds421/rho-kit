#!/usr/bin/env bash
# Verify every workspace module's go.mod/go.sum is what `go mod tidy`
# would produce. Catches the failure mode where a module's source code
# imports a kit package but the module's go.mod does NOT have a require
# line for that kit module — go.work + a local `replace` masks the
# missing require during local dev, but a publish-time tidy then has to
# invent a version and (if the dep is not yet tagged) falls back to a
# pseudo-version pin. That's the bug class that produced the v2.0.0
# pseudo-version pins in crypto, resilience, security, etc.
#
# Cost: ~30s per module on cold cache, near-instant with warm cache.
# Run after every dep change and before every tag.

set -euo pipefail
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

# Enumerate workspace modules from go.work (single source of truth).
modules=$(awk '/^use \(/,/^\)/' go.work | grep -E '^[[:space:]]+\./' | sed 's|^[[:space:]]*\./||; s|/$||' | sort -u)

# Also tidy-check tracked go.mod files that live OUTSIDE go.work — e.g.
# the stdlib-only tools/check-* helper modules. They are excluded from
# go.work on purpose (so they don't pull into the workspace graph), but
# any future dep added to one of them would otherwise never be
# tidy-verified, reintroducing the missing-require pseudo-version bug
# class this gate exists to prevent. Walk git-tracked go.mod paths with
# GOWORK=off and append the ones not already enumerated above.
tracked_modules=$(git ls-files '*go.mod' | sed 's|/go.mod$||; s|^\./||' | grep -v '^go.mod$' | sort -u)
extra_modules=$(comm -23 <(printf '%s\n' "$tracked_modules") <(printf '%s\n' "$modules"))
if [ -n "$extra_modules" ]; then
  modules=$(printf '%s\n%s\n' "$modules" "$extra_modules" | sort -u)
fi

fail=0
stale=()
errors=()

while IFS= read -r dir; do
  [ -z "$dir" ] && continue
  # go mod tidy -diff prints the unified diff that would be applied
  # to stdout; empty stdout means the module is already tidy-clean.
  # A non-zero exit with no diff is a real tidy failure and must not be
  # silently accepted as clean.
  tidy_err=$(mktemp)
  tidy_status=0
  diff_out=$(cd "$dir" && GOWORK=off go mod tidy -diff 2>"$tidy_err") || tidy_status=$?
  if [ -n "$diff_out" ]; then
    stale+=("$dir")
    fail=1
  elif [ "$tidy_status" -ne 0 ]; then
    errors+=("$dir: $(tr '\n' ' ' < "$tidy_err")")
    fail=1
  fi
  rm -f "$tidy_err"
done <<< "$modules"

if [ "$fail" -ne 0 ]; then
  echo "tidy check FAILED" >&2
  if [ "${#stale[@]}" -gt 0 ]; then
    echo "The following modules' go.mod/go.sum files are not tidy-clean:" >&2
    printf '  %s\n' "${stale[@]}" >&2
  fi
  if [ "${#errors[@]}" -gt 0 ]; then
    echo "The following modules could not be tidied:" >&2
    printf '  %s\n' "${errors[@]}" >&2
  fi
  echo "" >&2
  echo "Run 'cd <dir> && GOWORK=off go mod tidy' in each, commit the diff, and re-run." >&2
  exit 1
fi

echo "tidy check OK ($(echo "$modules" | wc -l | tr -d ' ') workspace modules clean)"
