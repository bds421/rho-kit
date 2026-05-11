#!/usr/bin/env bash
set -euo pipefail

# Pre-tag gate: fail if any go.mod in the workspace still requires an
# internal rho-kit module at v0.0.0, or locally replaces an internal
# package path that is not a real module.
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
MODULE_PREFIX='github.com/bds421/rho-kit/'

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

modules_file="$(mktemp)"
replaces_file="$(mktemp)"
stale_replaces_file="$(mktemp)"
trap 'rm -f "$modules_file" "$replaces_file" "$stale_replaces_file"' EXIT

find . -name go.mod \
  -not -path '*/node_modules/*' \
  -not -path '*/.claude/*' \
  -not -path '*/dist/*' \
  -print0 |
  xargs -0 sed -n 's/^module //p' |
  sort -u > "$modules_file"

find . -name go.mod \
  -not -path '*/node_modules/*' \
  -not -path '*/.claude/*' \
  -not -path '*/dist/*' \
  -print0 |
  xargs -0 awk -v prefix="$MODULE_PREFIX" '
    /^replace[[:space:]]*\(/ {
      in_replace_block = 1
      next
    }
    in_replace_block && /^[[:space:]]*\)/ {
      in_replace_block = 0
      next
    }
    in_replace_block && /=>/ {
      old_path = $1
      if (index(old_path, prefix) == 1) {
        printf "%s:%d:%s\n", FILENAME, FNR, old_path
      }
      next
    }
    /^replace[[:space:]]+/ && /=>/ {
      old_path = $2
      if (index(old_path, prefix) == 1) {
        printf "%s:%d:%s\n", FILENAME, FNR, old_path
      }
    }
  ' > "$replaces_file"

while IFS=: read -r file line old_path; do
  if ! grep -qxF "$old_path" "$modules_file"; then
    printf "%s:%s:%s\n" "$file" "$line" "$old_path" >> "$stale_replaces_file"
  fi
done < "$replaces_file"

if [[ -s "$stale_replaces_file" ]]; then
  echo "ERROR: pre-tag gate failed — internal replace directives target package paths, not modules:"
  echo
  cat "$stale_replaces_file"
  echo
  echo "Go replace directives operate on module paths only. A replace for"
  echo "github.com/bds421/rho-kit/data/v2/foo is ignored when foo is a package"
  echo "inside github.com/bds421/rho-kit/data/v2, and downstream consumers"
  echo "do not inherit local replaces. Replace the parent module path or remove"
  echo "the stale directive."
  exit 1
fi

echo "OK: all internal replace directives point at workspace modules."

expected_go_version="$(sed -n 's/^go //p' go.work | head -n 1)"
if [[ -z "$expected_go_version" ]]; then
  echo "ERROR: pre-tag gate failed — go.work does not declare a Go version."
  exit 1
fi

go_version_mismatches="$(find . -name go.mod \
  -not -path '*/node_modules/*' \
  -not -path '*/.claude/*' \
  -not -path '*/dist/*' \
  -print0 |
  xargs -0 awk -v expected="$expected_go_version" '
    /^go[[:space:]]+/ {
      if ($2 != expected) {
        printf "%s:%d:go %s\n", FILENAME, FNR, $2
      }
    }
  ')"

if [[ -n "$go_version_mismatches" ]]; then
  echo "ERROR: pre-tag gate failed — module Go directives do not match go.work (${expected_go_version}):"
  echo
  echo "$go_version_mismatches"
  echo
  echo "Normalize module go.mod files before tagging so every published module"
  echo "advertises the same patched Go baseline."
  exit 1
fi

echo "OK: all module Go directives match go.work (${expected_go_version})."
