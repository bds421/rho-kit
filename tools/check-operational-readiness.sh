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
  module_path="$(cd "$dir" && go list -m -f '{{.Path}}')"
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
