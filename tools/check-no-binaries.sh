#!/usr/bin/env bash
# check-no-binaries.sh — reject tracked binary artifacts outside fixture dirs.
#
# Audit FR-001 prevention. The rho-kit repo had Mach-O executables
# (kit-new, kit-verify) tracked in source control which created release
# bloat, ambiguous provenance, and the risk of stale binaries being
# invoked instead of reproducible builds. This guard runs in `make ci`
# and refuses to pass when any tracked file is one of:
#
#   - Mach-O / ELF / Windows PE executable (file(1) classification)
#   - Larger than 1 MB and not classified as text
#
# UNLESS the file lives under a directory in the ALLOWLIST below
# (test fixtures that legitimately need binary blobs).
set -euo pipefail

# Directories where binary fixtures are allowed.
ALLOWLIST=(
    "tools/check-no-binaries-fixtures"
    # Add explicit fixture paths here if a test requires committed binaries.
)

REJECT_BINARIES=()
WARN_LARGE=()
SCAN_SCOPE="full repository"
SCAN_LIST=""

allowed() {
    local path="$1"
    local prefix
    for prefix in "${ALLOWLIST[@]}"; do
        if [[ "$path" == "$prefix"/* ]]; then
            return 0
        fi
    done
    return 1
}

# A protected main branch has already passed the full-repository scan. On a
# pull request, only added/changed/renamed files can introduce a new binary.
# CI_BASE_REF is deliberately unset on main, so the authoritative post-merge
# build still scans everything. Changes to this guard itself force a full scan.
if [[ -n "${CI_BASE_REF:-}" ]]; then
    if ! git cat-file -e "${CI_BASE_REF}^{commit}" 2>/dev/null; then
        echo "tracked-binary check FAILED — CI_BASE_REF is not a commit: ${CI_BASE_REF}" >&2
        exit 1
    fi
    if git diff --name-only "${CI_BASE_REF}...HEAD" -- tools/check-no-binaries.sh | grep -q .; then
        SCAN_LIST=$(git ls-files)
    else
        SCAN_SCOPE="changes since ${CI_BASE_REF}"
        SCAN_LIST=$(git diff --name-only --diff-filter=ACMR "${CI_BASE_REF}...HEAD")
    fi
else
    SCAN_LIST=$(git ls-files)
fi

# Walk the selected tracked files. Build artifacts that are gitignored are not
# the concern here.
scanned=0
while IFS= read -r path; do
    [[ -n "$path" ]] || continue
    if allowed "$path"; then
        continue
    fi
    # Skip non-existent (rare; handles deletions in flight).
    [[ -f "$path" ]] || continue
    scanned=$((scanned + 1))

    mime=$(file --mime-type --brief -- "$path" 2>/dev/null || echo unknown)
    case "$mime" in
        application/x-mach-binary|application/x-executable|application/x-pie-executable|application/x-dosexec|application/vnd.microsoft.portable-executable)
            REJECT_BINARIES+=("$path ($mime)")
            ;;
    esac

    # Anything > 1 MB that isn't text/source is suspicious. We only
    # warn for those — fixtures may legitimately want PNG screenshots,
    # PDF samples, etc., but reviewers should explicitly allow-list
    # those. Reject only the executable cases above.
    size=$(stat -f%z -- "$path" 2>/dev/null || stat -c%s -- "$path" 2>/dev/null || echo 0)
    if (( size > 1048576 )); then
        case "$mime" in
            text/*|application/json|application/xml|application/yaml|application/x-yaml)
                : # OK: large but text
                ;;
            *)
                WARN_LARGE+=("$path ($mime, $((size/1024)) KiB)")
                ;;
        esac
    fi
done <<< "$SCAN_LIST"

if (( ${#REJECT_BINARIES[@]} > 0 )); then
    printf 'tracked-binary check FAILED — refusing executable artifacts in source control:\n' >&2
    printf '  %s\n' "${REJECT_BINARIES[@]}" >&2
    printf '\nMove these to dist/ (built locally) or, if a test fixture, add the directory to ALLOWLIST in tools/check-no-binaries.sh.\n' >&2
    exit 1
fi

if (( ${#WARN_LARGE[@]} > 0 )); then
    printf 'tracked-binary check WARNING — large non-text files committed:\n' >&2
    printf '  %s\n' "${WARN_LARGE[@]}" >&2
    printf '\n(Warning only; reject only executable types. Move to fixture allowlist if intentional.)\n' >&2
fi

echo "tracked-binary check OK (${scanned} files scanned; ${SCAN_SCOPE})"
