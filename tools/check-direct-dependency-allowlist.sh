#!/usr/bin/env bash
# check-direct-dependency-allowlist.sh - reject unreviewed direct Go deps.
#
# GAP-10 prevention. Every non-rho-kit direct require across workspace
# go.mod files must be listed in docs/audit/dependency-allowlist.txt.
# Transitive deps are still covered by govulncheck; this guard
# keeps new direct trust roots from appearing without an explicit policy diff.
set -euo pipefail

ALLOWLIST_FILE="${1:-docs/audit/dependency-allowlist.txt}"
INTERNAL_PREFIX="github.com/bds421/rho-kit/"

if [[ ! -f "$ALLOWLIST_FILE" ]]; then
    echo "dependency allowlist check FAILED - missing $ALLOWLIST_FILE" >&2
    exit 1
fi

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

allowed="$tmpdir/allowed"
raw_actual="$tmpdir/raw-actual"
actual="$tmpdir/actual"
unknown="$tmpdir/unknown"
unknown_locations="$tmpdir/unknown-locations"
stale="$tmpdir/stale"

awk '
    {
        line = $0
        sub(/#.*/, "", line)
        gsub(/^[[:space:]]+|[[:space:]]+$/, "", line)
        if (line == "") {
            next
        }
        if (line ~ /[[:space:]]/) {
            print FILENAME ":" FNR ": allowlist entries must be exact module paths" > "/dev/stderr"
            bad = 1
            next
        }
        print line
    }
    END {
        if (bad) {
            exit 2
        }
    }
' "$ALLOWLIST_FILE" | sort -u > "$allowed"

git ls-files -co --exclude-standard -- '*go.mod' |
    while IFS= read -r gomod; do
        awk '
            function emit(module) {
                if (module != "") {
                    print module "\t" FILENAME ":" FNR
                }
            }
            /^require[[:space:]]+\(/ { inreq=1; next }
            inreq && /^\)/ { inreq=0; next }
            inreq {
                if ($0 !~ /\/\/[[:space:]]*indirect/ && $1 !~ /^\/\//) {
                    emit($1)
                }
                next
            }
            /^require[[:space:]]+/ {
                if ($0 !~ /\/\/[[:space:]]*indirect/) {
                    emit($2)
                }
            }
        ' "$gomod"
    done |
    awk -F '\t' -v internal="$INTERNAL_PREFIX" 'index($1, internal) != 1 { print }' |
    sort -u > "$raw_actual"

cut -f1 "$raw_actual" | sort -u > "$actual"
comm -23 "$actual" "$allowed" > "$unknown"
comm -23 "$allowed" "$actual" > "$stale"
awk -F '\t' 'NR==FNR { bad[$1]=1; next } bad[$1] { print $2 ": " $1 }' "$unknown" "$raw_actual" > "$unknown_locations"

if [[ -s "$unknown_locations" || -s "$stale" ]]; then
    echo "dependency allowlist check FAILED" >&2
    if [[ -s "$unknown_locations" ]]; then
        printf '\nUnreviewed direct dependencies:\n' >&2
        sed 's/^/  /' "$unknown_locations" >&2
        printf '\nAdd each module to %s in the same PR as the dependency change, with reviewer sign-off.\n' "$ALLOWLIST_FILE" >&2
    fi
    if [[ -s "$stale" ]]; then
        printf '\nStale allowlist entries no longer used as direct dependencies:\n' >&2
        sed 's/^/  /' "$stale" >&2
        printf '\nRemove stale entries so the allowlist remains an exact review ledger.\n' >&2
    fi
    exit 1
fi

count=$(wc -l < "$actual" | tr -d ' ')
echo "dependency allowlist check OK (${count} direct external deps approved)"
