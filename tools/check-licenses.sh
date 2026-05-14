#!/usr/bin/env bash
# Walk the workspace's transitive Go dependency graph and assert that every
# resolved module's license declaration is on the SUPPLY_CHAIN.md §8.1
# allowlist. Runs from CI (.github/workflows/licenses.yml) and locally via
# `make check-licenses`.
#
# Design choices:
#   * `go-licenses` is pinned. We invoke it via `go run` so contributors do
#     not need a separate install step and CI does not need a cache layer.
#   * The script tolerates `go-licenses` warnings ("module not found in
#     GOPATH", stdlib resolution failures, etc.) on stderr; those routinely
#     occur for indirect modules whose source has not been vendored. Hard
#     failures still surface because the resulting CSV will be empty / short
#     and the allowlist check is invoked on whatever is emitted.
#   * The script is the single source of truth for the allowlist contents.
#     The values below MUST match the table in docs/audit/SUPPLY_CHAIN.md
#     §8.1. Changing one without the other will be caught in review.
#   * Forbidden categories from §8.1 (GPL / AGPL / proprietary / unknown)
#     are not enumerated here — any license string outside the allowlist
#     fails the gate, which is the intended behaviour.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

GO_LICENSES_VERSION="v1.6.0"

# Allowlist from docs/audit/SUPPLY_CHAIN.md §8.1.
# MIT, Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC are unconditionally
# allowed. MPL-2.0 is "case-by-case" per the policy but is currently
# in use by `github.com/hashicorp/*` style modules the kit ships with;
# we accept it here and document the case-by-case review caveat in
# SUPPLY_CHAIN.md §8.1. LGPL is policy-allowed but not in the current
# graph; if it appears the reviewer must extend the allowlist
# deliberately, with a corresponding §8.1 update in the same PR.
ALLOWED=(
  "Apache-2.0"
  "MIT"
  "BSD-2-Clause"
  "BSD-3-Clause"
  "ISC"
  "MPL-2.0"
)

# Skip patterns. go-licenses occasionally classifies the standard library
# (or a vendored Go toolchain module) as "Unknown". We never need to gate
# on those because the toolchain itself is BSD-3-Clause and is verified at
# the supply-chain level (§4 / §6 of SUPPLY_CHAIN.md), not via go-licenses.
SKIP_MODULE_PATTERNS=(
  "^std$"
  "^cmd/"
  "^golang.org/toolchain"
  "^github.com/bds421/rho-kit"
)

# Modules that are workspace-internal — listed here for explicitness; the
# pattern list above already covers them but we treat the leading prefix
# match as load-bearing.
INTERNAL_PREFIX="github.com/bds421/rho-kit"

WORKSPACE_MODULES=()
while IFS= read -r dir; do
  [[ -z "$dir" ]] && continue
  WORKSPACE_MODULES+=("$dir")
done < <(sed -n '/^use (/,/^)/{ s/^[[:space:]]*\.\/\(.*\)/\1/p; }' go.work | grep -v '^\.$' || true)

if [[ "${#WORKSPACE_MODULES[@]}" -eq 0 ]]; then
  echo "FAIL: no workspace modules discovered in go.work" >&2
  exit 1
fi

WORK_DIR="$(mktemp -d)"
trap 'rm -rf "$WORK_DIR"' EXIT
CSV_OUT="${WORK_DIR}/licenses.csv"
: > "$CSV_OUT"

scanner_failed=0
for mod_dir in "${WORKSPACE_MODULES[@]}"; do
  echo "==> Licenses: ${mod_dir}"
  # go-licenses emits per-package rows; we accumulate and de-duplicate
  # at the end. The exit status MUST be captured per module — wave 66
  # caught that the previous `|| true` swallowed scanner failures and
  # made the gate weaker than the release policy claimed. A scanner
  # failure can drop dependency rows silently; we now fail the gate
  # but keep collecting other modules' rows for the diagnostic output.
  mod_csv="${WORK_DIR}/${mod_dir//\//_}.csv"
  mod_err="${WORK_DIR}/${mod_dir//\//_}.err"
  set +e
  (
    cd "$mod_dir" && \
    go run "github.com/google/go-licenses@${GO_LICENSES_VERSION}" csv ./... 2>"$mod_err"
  ) > "$mod_csv"
  rc=$?
  set -e
  if [[ "$rc" -ne 0 ]]; then
    scanner_failed=1
    echo "FAIL: go-licenses csv exited with status $rc for $mod_dir" >&2
    if [[ -s "$mod_err" ]]; then
      sed -e 's/^/    /' "$mod_err" >&2
    fi
  fi
  cat "$mod_csv" >> "$CSV_OUT"
done

if [[ "$scanner_failed" == 1 ]]; then
  echo "" >&2
  echo "License gate cannot certify the dependency graph because at least one" >&2
  echo "module scan failed above. Fix the underlying go-licenses error before" >&2
  echo "treating the gate as authoritative." >&2
  exit 1
fi

# De-duplicate (module, url, license) triples.
sort -u "$CSV_OUT" -o "$CSV_OUT"

fail=0
seen_any=0
while IFS=, read -r module url license; do
  module="${module%%[[:space:]]*}"
  license="${license##[[:space:]]}"
  [[ -z "$module" ]] && continue
  seen_any=1

  skip=0
  for pat in "${SKIP_MODULE_PATTERNS[@]}"; do
    if [[ "$module" =~ $pat ]]; then
      skip=1
      break
    fi
  done
  if [[ "$skip" == 1 ]]; then
    continue
  fi
  # Internal workspace modules: covered by the kit's own LICENSE.md,
  # not by go-licenses.
  if [[ "$module" == "$INTERNAL_PREFIX"* ]]; then
    continue
  fi

  matched=0
  for a in "${ALLOWED[@]}"; do
    if [[ "$license" == "$a" ]]; then
      matched=1
      break
    fi
  done

  if [[ "$matched" == 0 ]]; then
    echo "FAIL: ${module} — license '${license}' not in SUPPLY_CHAIN.md §8.1 allowlist" >&2
    fail=1
  fi
done < "$CSV_OUT"

if [[ "$seen_any" == 0 ]]; then
  echo "WARN: go-licenses produced no rows; the toolchain or GOPATH cache" >&2
  echo "      may be cold. Re-run after \`make build\` populates the cache." >&2
fi

if [[ "$fail" == 1 ]]; then
  echo "" >&2
  echo "License gate failed. Either:" >&2
  echo "  - bump / replace the offending dep with an allowlisted license, or" >&2
  echo "  - extend the allowlist in docs/audit/SUPPLY_CHAIN.md §8.1 and" >&2
  echo "    tools/check-licenses.sh in the same PR (security review required)." >&2
  exit 1
fi

echo "OK: all license declarations on the §8.1 allowlist."
