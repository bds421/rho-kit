#!/usr/bin/env bash
set -euo pipefail

# Static pre-tag gate for the Go multi-module workspace.
#
# It checks the invariants that matter to downstream Go consumers:
#   - no internal rho-kit module is pinned at v0.0.0
#   - optional EXPECTED_INTERNAL_VERSION pins are consistent when requested
#   - local internal replace directives point at real workspace modules
#   - all module go directives match go.work
#
# Why: local `replace` directives let the workspace build even when an
# internal require is wrong. Downstream consumers do not inherit those replaces;
# they resolve the `require` versions from module tags.

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

MODULE_PREFIX='github.com/bds421/rho-kit/'
INTERNAL_V0_PATTERN='github.com/bds421/rho-kit/.+ v0\.0\.0'
EXPECTED_INTERNAL_VERSION="${EXPECTED_INTERNAL_VERSION:-}"
FORBID_INTERNAL_REPLACES="${FORBID_INTERNAL_REPLACES:-0}"

find_go_mods() {
  find . -name go.mod \
    -not -path '*/.claude/*' \
    -not -path '*/dist/*' \
    -print0
}

if command -v rg >/dev/null 2>&1; then
  v0_matches=$(rg --no-heading --line-number "$INTERNAL_V0_PATTERN" \
    --glob 'go.mod' \
    --glob '!**/.claude/**' \
    --glob '!**/dist/**' \
    || true)
else
  v0_matches=$(find_go_mods |
    xargs -0 grep -nE "$INTERNAL_V0_PATTERN" /dev/null 2>/dev/null || true)
fi

if [[ -n "$v0_matches" ]]; then
  echo "ERROR: pre-tag gate failed — internal rho-kit modules still pinned at v0.0.0:"
  echo
  echo "$v0_matches"
  echo
  echo "These pins must be rewritten to real released versions before tagging."
  echo "Local replace directives mask this in the workspace, but downstream"
  echo "consumers do not inherit them."
  exit 1
fi

echo "OK: no internal rho-kit modules pinned at v0.0.0."

modules_file="$(mktemp)"
replaces_file="$(mktemp)"
stale_replaces_file="$(mktemp)"
internal_requires_file="$(mktemp)"
unexpected_internal_versions_file="$(mktemp)"
trap 'rm -f "$modules_file" "$replaces_file" "$stale_replaces_file" "$internal_requires_file" "$unexpected_internal_versions_file"' EXIT

find_go_mods |
  xargs -0 sed -n 's/^module //p' |
  sort -u > "$modules_file"

find_go_mods |
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
  echo "Go replace directives operate on module paths only. Replace the parent"
  echo "module path or remove the stale directive."
  exit 1
fi

echo "OK: all internal replace directives point at workspace modules."

if [[ "$FORBID_INTERNAL_REPLACES" == "1" && -s "$replaces_file" ]]; then
  echo "ERROR: pre-tag gate failed — internal replace directives remain:"
  echo
  cat "$replaces_file"
  echo
  echo "For a dependency-ordered release with committed internal go.sum"
  echo "checksums, drop local internal replaces from modules before tagging."
  echo "Use go.work for local development instead."
  exit 1
fi

find_go_mods |
  xargs -0 awk -v prefix="$MODULE_PREFIX" '
    /^require[[:space:]]*\(/ {
      in_require_block = 1
      next
    }
    in_require_block && /^[[:space:]]*\)/ {
      in_require_block = 0
      next
    }
    in_require_block && index($1, prefix) == 1 {
      printf "%s:%d:%s:%s\n", FILENAME, FNR, $1, $2
      next
    }
    /^require[[:space:]]+/ && index($2, prefix) == 1 {
      printf "%s:%d:%s:%s\n", FILENAME, FNR, $2, $3
    }
  ' > "$internal_requires_file"

if [[ -n "$EXPECTED_INTERNAL_VERSION" ]]; then
  while IFS=: read -r file line mod version; do
    if [[ "$version" != "$EXPECTED_INTERNAL_VERSION" ]]; then
      printf "%s:%s:%s %s\n" "$file" "$line" "$mod" "$version" >> "$unexpected_internal_versions_file"
    fi
  done < "$internal_requires_file"

  if [[ -s "$unexpected_internal_versions_file" ]]; then
    echo "ERROR: pre-tag gate failed — internal rho-kit requires do not match EXPECTED_INTERNAL_VERSION=${EXPECTED_INTERNAL_VERSION}:"
    echo
    cat "$unexpected_internal_versions_file"
    echo
    echo "For a lockstep release, every internal require must point at the"
    echo "version that will be tagged for every workspace module."
    exit 1
  fi

  echo "OK: all internal rho-kit requires match ${EXPECTED_INTERNAL_VERSION}."
fi

expected_go_version="$(sed -n 's/^go //p' go.work | head -n 1)"
if [[ -z "$expected_go_version" ]]; then
  echo "ERROR: pre-tag gate failed — go.work does not declare a Go version."
  exit 1
fi

go_version_mismatches="$(find_go_mods |
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
