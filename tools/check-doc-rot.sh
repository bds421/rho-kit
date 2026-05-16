#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

exec env GOWORK=off go run "$REPO_ROOT/tools/check-doc-rot/main.go" \
  -root "$REPO_ROOT" \
  "$@"
