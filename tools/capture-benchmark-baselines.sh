#!/usr/bin/env bash
# capture-benchmark-baselines.sh - record reproducible go test benchmark output.
#
# The output files are raw `go test -bench` text so they can be fed directly to
# cmd/kit-bench-gate as baselines. Only workspace modules with real benchmark
# functions are captured.
set -euo pipefail

version="${BENCH_VERSION:-v2.0.0}"
count="${BENCH_COUNT:-5}"
outdir="${BENCH_OUT_DIR:-docs/release/benchmarks/${version}}"

case "$outdir" in
    docs/release/benchmarks/*) ;;
    *)
        echo "BENCH_OUT_DIR must stay under docs/release/benchmarks" >&2
        exit 2
        ;;
esac

if ! [[ "$count" =~ ^[1-9][0-9]*$ ]]; then
    echo "BENCH_COUNT must be a positive integer" >&2
    exit 2
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

git_revision="$(git rev-parse HEAD 2>/dev/null || printf unknown)"
source_status="$(git status --porcelain -- . ":(exclude)$outdir" 2>/dev/null || true)"
if [[ -n "$source_status" ]]; then
    tree_state="dirty"
else
    tree_state="clean"
fi

# Canonical baselines must capture against a tracked, committed working
# tree so the `kit-bench-gate -baseline` artefact a downstream consumer
# loads is reproducible from the recorded git revision. Refuse to record
# a dirty tree unless the operator opts in via ALLOW_DIRTY_TREE=1, which
# is intended for local exploration only — never for release artefacts
# committed under docs/release/benchmarks (L-171).
if [[ "$tree_state" == "dirty" && "${ALLOW_DIRTY_TREE:-0}" != "1" ]]; then
    printf >&2 'capture-benchmark-baselines: working tree is dirty; refusing to capture canonical baselines.\n'
    printf >&2 'Either commit/stash the changes or set ALLOW_DIRTY_TREE=1 for an exploratory (non-canonical) run.\n'
    printf >&2 'Untracked/modified files outside %s:\n' "$outdir"
    printf >&2 '%s\n' "$source_status"
    exit 1
fi

modules_file="$tmpdir/modules"
sed -n '/^use (/,/^)/{ s/^[[:space:]]*\.\/\(.*\)/\1/p; }' go.work |
    grep -v '^\.' > "$modules_file"

mkdir -p "$outdir"
manifest="$outdir/MANIFEST.md"

{
    printf '# %s benchmark baselines\n\n' "$version"
    printf 'Generated: `%s`\n\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf -- '- Source Git revision: `%s`\n' "$git_revision"
    printf -- '- Source working tree: `%s`\n' "$tree_state"
    printf -- '- Go: `%s`\n' "$(go version)"
    printf -- '- GOOS/GOARCH: `%s/%s`\n' "$(go env GOOS)" "$(go env GOARCH)"
    printf -- '- Command shape: `go test -run=^$ -bench=. -benchmem -count=%s ./...`\n\n' "$count"
    printf 'These files are intended as `kit-bench-gate -baseline` inputs. Refresh\n'
    printf 'them on release-candidate hardware before tagging if the machine changes.\n\n'
    printf 'Source metadata is captured before benchmark output files are rewritten;\n'
    printf 'the benchmark output directory is ignored when computing source-tree cleanliness.\n\n'
    printf '## Captured Modules\n\n'
} > "$manifest"

captured=0
while IFS= read -r dir; do
    if ! rg -q '^func Benchmark' "$dir" --glob '*_test.go'; then
        continue
    fi
    safe="${dir//\//__}"
    outfile="$outdir/${safe}.bench"
    printf '==> Benchmark baseline %s\n' "$dir"
    {
        printf '# module: %s\n' "$dir"
        printf '# command: go test -run=^$ -bench=. -benchmem -count=%s ./...\n' "$count"
        printf '# generated: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
        (cd "$dir" && go test -run=^$ -bench=. -benchmem -count="$count" ./...)
    } > "$outfile"
    printf -- '- `%s` -> `%s`\n' "$dir" "$(basename "$outfile")" >> "$manifest"
    captured=$((captured + 1))
done < "$modules_file"

if (( captured == 0 )); then
    echo "no benchmark functions found in go.work modules" >&2
    exit 1
fi

printf '\nCaptured %d benchmark module(s) under %s\n' "$captured" "$outdir"
