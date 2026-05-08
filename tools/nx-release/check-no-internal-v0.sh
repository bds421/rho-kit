#!/usr/bin/env bash
set -euo pipefail

# Pre-tag gate: fail if any go.mod in the workspace still requires an
# internal rho-kit module at v0.0.0.
#
# Why: local `replace` directives let the workspace tidy/build with
# v0.0.0 pins, but those replaces are ignored by downstream consumers
# of a published module. Tagging a module while it still pins
# internal modules at v0.0.0 ships a broken release.
#
# Run AFTER `nx release version` (which rewrites require directives
# to real versions) and BEFORE `git tag` / `git push`.

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

PATTERN='github.com/bds421/rho-kit/.+ v0\.0\.0'

if command -v rg >/dev/null 2>&1; then
  matches=$(rg --no-heading --line-number "$PATTERN" \
    --glob 'go.mod' \
    --glob '!**/node_modules/**' \
    --glob '!**/.claude/**' \
    --glob '!**/dist/**' \
    || true)
else
  # Fallback to grep when ripgrep is not installed.
  matches=$(find . -name go.mod \
    -not -path '*/node_modules/*' \
    -not -path '*/.claude/*' \
    -not -path '*/dist/*' \
    -print0 |
    xargs -0 grep -nE "$PATTERN" /dev/null 2>/dev/null || true)
fi

if [[ -n "$matches" ]]; then
  echo "ERROR: pre-tag gate failed — internal rho-kit modules still pinned at v0.0.0:"
  echo
  echo "$matches"
  echo
  echo "These pins must be rewritten to real released versions before tagging."
  echo "Local replace directives mask this in the workspace but downstream"
  echo "consumers do not inherit them. Run 'nx release version' to bump"
  echo "internal require directives, then re-run this gate."
  exit 1
fi

echo "OK: no internal rho-kit modules pinned at v0.0.0."
