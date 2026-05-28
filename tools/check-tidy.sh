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

fail=0
stale=()
while IFS= read -r dir; do
  [ -z "$dir" ] && continue
  # go mod tidy -diff prints the unified diff that would be applied
  # to stdout; empty stdout means the module is already tidy-clean.
  # stderr carries download/progress noise from cold cache and is
  # discarded so a warm-vs-cold cache doesn't change the gate result.
  diff_out=$(cd "$dir" && GOWORK=off go mod tidy -diff 2>/dev/null || true)
  if [ -n "$diff_out" ]; then
    stale+=("$dir")
    fail=1
  fi
done <<< "$modules"

if [ "$fail" -ne 0 ]; then
  echo "tidy check FAILED — the following modules' go.mod/go.sum are not tidy-clean:" >&2
  printf '  %s\n' "${stale[@]}" >&2
  echo "" >&2
  echo "Run 'cd <dir> && GOWORK=off go mod tidy' in each, commit the diff, and re-run." >&2
  exit 1
fi

echo "tidy check OK ($(echo "$modules" | wc -l | tr -d ' ') workspace modules clean)"
