#!/usr/bin/env bash
set -euo pipefail

doc="docs/release/OPERATIONAL_READINESS_V2.md"

if [[ ! -f "$doc" ]]; then
  echo "missing $doc" >&2
  exit 1
fi

required_sections=(
  "## Credential And Key Rotation"
  "## TLS Material Rotation"
  "## Startup And Configuration"
  "## Shutdown And Draining"
  "## Backpressure And Bounded Work"
  "## Observability And Metric Contracts"
  "## Health And Readiness"
  "## Migrations And Rollback"
  "## Dependency And Runtime Gates"
  "## Module Coverage Matrix"
)

for section in "${required_sections[@]}"; do
  if ! grep -Fq "$section" "$doc"; then
    echo "$doc missing required section: $section" >&2
    exit 1
  fi
done

missing=()
count=0
while IFS= read -r dir; do
  [[ -n "$dir" ]] || continue
  if [[ ! -f "$dir/go.mod" ]]; then
    echo "missing $dir/go.mod for workspace entry" >&2
    exit 1
  fi
  # Parse the module path directly from go.mod. `go list -m` would honour
  # go.work and print every workspace module's path, which silently turns
  # this coverage check into a tautology when grep -Fq matches against any
  # one of those concatenated lines.
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

echo "operational readiness check OK ($count modules covered)"
