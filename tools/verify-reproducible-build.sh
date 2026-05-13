#!/usr/bin/env bash
# verify-reproducible-build.sh — assert that two builds of a cmd binary,
# from the same source tree, produce byte-identical artefacts.
#
# Usage:
#   bash tools/verify-reproducible-build.sh [BIN]   # default BIN=kit-doctor
#
# What it does:
#   1. Copies the entire repository into two temp directories.
#   2. Runs `make release-bin BIN=<bin>` in each.
#   3. Compares the resulting sha256 hashes.
#
# This is intentionally opt-in: `cp -R` over the 67-module workspace is not
# cheap. Use it before pushing a change that could affect determinism (Go
# toolchain bump, ldflags edit, switch to CGo, etc.) — or to convince
# yourself the reproducibility promise in docs/audit/SUPPLY_CHAIN.md §4
# still holds on your platform.
set -euo pipefail

BIN="${1:-kit-doctor}"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

if [ ! -d "cmd/$BIN" ]; then
    echo "FAIL: cmd/$BIN does not exist" >&2
    exit 1
fi

tmp1=$(mktemp -d)
tmp2=$(mktemp -d)
trap 'rm -rf "$tmp1" "$tmp2"' EXIT

echo "==> Copying repository to $tmp1"
cp -R . "$tmp1"
echo "==> Copying repository to $tmp2"
cp -R . "$tmp2"

echo "==> Build 1"
(cd "$tmp1" && make release-bin BIN="$BIN" >/dev/null)
echo "==> Build 2"
(cd "$tmp2" && make release-bin BIN="$BIN" >/dev/null)

hash_of() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1" | awk '{print $1}'
    else
        shasum -a 256 "$1" | awk '{print $1}'
    fi
}

h1=$(hash_of "$tmp1/dist/cmd/$BIN/$BIN")
h2=$(hash_of "$tmp2/dist/cmd/$BIN/$BIN")

if [ "$h1" != "$h2" ]; then
    echo "FAIL: hashes differ for $BIN"
    echo "  build 1: $h1"
    echo "  build 2: $h2"
    exit 1
fi

echo "OK: reproducible build of $BIN sha256=$h1"
