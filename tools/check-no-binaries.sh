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

# Walk every tracked file. We only need ls-files; build artifacts that
# are gitignored are not the concern here.
while IFS= read -r path; do
    if allowed "$path"; then
        continue
    fi
    # Skip non-existent (rare; handles deletions in flight).
    [[ -f "$path" ]] || continue

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
done < <(git ls-files)

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

echo "tracked-binary check OK ($(git ls-files | wc -l | tr -d ' ') files scanned)"
