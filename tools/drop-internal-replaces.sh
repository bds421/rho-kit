#!/usr/bin/env bash
set -euo pipefail

# Drop local rho-kit replace directives from workspace modules.
#
# This is a release-branch helper. Normal local development should use go.work,
# not per-module internal replaces, once the dependency-ordered release starts.

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

modules_file="$(mktemp)"
paths_file="$(mktemp)"
trap 'rm -f "$modules_file" "$paths_file"' EXIT

awk '/^use \(/,/^\)/ {
  if ($1 ~ /^\.\//) {
    dir = $1
    sub(/^\.\//, "", dir)
    print dir
  }
}' go.work | sort -u > "$modules_file"

while IFS= read -r dir; do
  sed -n 's/^module //p' "$dir/go.mod"
done < "$modules_file" | sort -u > "$paths_file"

while IFS= read -r dir; do
  while IFS= read -r module_path; do
    (cd "$dir" && go mod edit -dropreplace="$module_path")
  done < "$paths_file"
done < "$modules_file"

echo "Dropped internal replace directives from workspace go.mod files."
