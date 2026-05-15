#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# GOWORK=off so the tool's own go.mod is honoured — the tool lives
# outside the workspace because it consumes Go AST (stdlib) and
# should never re-export anything into release surface.
exec env GOWORK=off go run "$REPO_ROOT/tools/check-dashboard-labels/main.go" \
  -repo "$REPO_ROOT" \
  "$@"
