#!/usr/bin/env bash
set -euo pipefail

# Proves every go.work workspace module has an exact-match coverage row in
# docs/release/API_FREEZE_V2.md. The companion to
# tools/check-operational-readiness.sh: a green check here means no module
# can land in v2 without an API-freeze decision row.
#
# We parse the module path directly from each module's go.mod rather than
# calling `go list -m`, because `go list -m` honours go.work in workspace
# mode and prints every workspace module's path — which would silently
# turn the grep into a tautology that any one of those concatenated lines
# could satisfy.

doc="docs/release/API_FREEZE_V2.md"

if [[ ! -f "$doc" ]]; then
  echo "missing $doc" >&2
  exit 1
fi

missing=()
count=0
while IFS= read -r dir; do
  [[ -n "$dir" ]] || continue
  if [[ ! -f "$dir/go.mod" ]]; then
    echo "missing $dir/go.mod for workspace entry" >&2
    exit 1
  fi
  module_path="$(awk '/^module[[:space:]]/ { print $2; exit }' "$dir/go.mod")"
  if [[ -z "$module_path" ]]; then
    echo "$dir/go.mod missing module directive" >&2
    exit 1
  fi
  count=$((count + 1))
  if ! grep -Fq "| \`$module_path\` |" "$doc"; then
    missing+=("$module_path")
  fi
done < <(sed -n '/^use (/,/^)/{ s/^[[:space:]]*\.\/\(.*\)/\1/p; }' go.work)

if (( ${#missing[@]} > 0 )); then
  echo "$doc is missing module coverage rows:" >&2
  printf '  %s\n' "${missing[@]}" >&2
  exit 1
fi

echo "api freeze coverage check OK ($count modules covered)"
