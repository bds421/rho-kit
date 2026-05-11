#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

args=()
if [[ -n "${RELEASE_VERSION:-}" ]]; then
  args+=("-version" "$RELEASE_VERSION")
fi
if [[ -n "${RELEASE_BASE_REF:-}" ]]; then
  args+=("-base" "$RELEASE_BASE_REF")
fi
if [[ -n "${RELEASE_MODE:-}" ]]; then
  args+=("-mode" "$RELEASE_MODE")
fi
if [[ -n "${RELEASE_FORMAT:-}" ]]; then
  args+=("-format" "$RELEASE_FORMAT")
fi
if [[ -n "${RELEASE_GLOBAL_CHANGES:-}" ]]; then
  args+=("-global" "$RELEASE_GLOBAL_CHANGES")
fi

if ((${#args[@]})); then
  exec env GOWORK=off go run "$REPO_ROOT/tools/release-planner/main.go" \
    -repo "$REPO_ROOT" \
    "${args[@]}" \
    "$@"
fi

exec env GOWORK=off go run "$REPO_ROOT/tools/release-planner/main.go" \
  -repo "$REPO_ROOT" \
  "$@"
