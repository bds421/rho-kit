#!/usr/bin/env bash
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

cd "$REPO_ROOT/tools/check-expand-only"
GOWORK=off go test -count=1 ./...
exec env GOWORK=off go run . -root "$REPO_ROOT" "$@"
