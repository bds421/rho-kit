#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# GOWORK=off so the tool's own go.mod is honoured — the tool lives
# outside the workspace because it consumes Go AST (stdlib) and is
# never re-exported into release surface.
exec env GOWORK=off go run "$REPO_ROOT/tools/check-fmt-errorf-wrap/main.go" \
  -root "$REPO_ROOT" \
  "$@"
