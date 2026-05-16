#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# Note: GOWORK is intentionally NOT disabled here — the inner
# `go test -bench` calls need the workspace to resolve kit packages
# that live in separate modules. The tool itself has no kit imports,
# so GOWORK doesn't pollute its compilation.
exec go run "$REPO_ROOT/tools/check-bench-regression/main.go" \
  -root "$REPO_ROOT" \
  "$@"
