#!/usr/bin/env bash
set -euo pipefail

# Hierarchical release for Go multi-module monorepo.
#
# Problem: go mod tidy needs published tags to compute go.sum checksums
# for internal modules. But tags are created during release.
#
# Solution: release in dependency order (leaf modules first). Each level
# gets its own commit with correct go.sum, tags, and push. By the time
# the next level runs, its dependencies are already published.
#
# Flow:
# 1. NX determines versions + updates go.mod + generates changelogs
# 2. Parse dependency graph from go.mod files
# 3. For each dependency level (bottom-up):
#    a. go mod tidy (GOWORK=off) — deps are already tagged
#    b. git commit (go.mod + go.sum + CHANGES.md)
#    c. git tag
#    d. git push
#
# Requires: NX has already run with git.commit=false, git.tag=false

REPO_ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$REPO_ROOT"

MODULE_PREFIX="github.com/bds421/rho-kit/"

# --- Step 1: Collect all modules and their new versions from git diff ---

echo "=== Collecting changed modules ==="

declare -A MODULE_VERSIONS  # module_path -> new_version
declare -A MODULE_DIRS      # module_path -> directory

# Read all modules from go.work
while IFS= read -r dir; do
  dir="${dir#./}"
  if [[ -z "$dir" ]]; then continue; fi

  mod_path=$(grep '^module ' "$dir/go.mod" | awk '{print $2}')
  MODULE_DIRS["$mod_path"]="$dir"

  # Check if this module has a version tag from NX (look for new CHANGES.md or go.mod changes)
  if git diff --name-only HEAD -- "$dir/CHANGES.md" "$dir/go.mod" | grep -q .; then
    # Extract version from CHANGES.md (NX writes "## X.Y.Z" at the top)
    if [[ -f "$dir/CHANGES.md" ]]; then
      version=$(grep -m1 '^## ' "$dir/CHANGES.md" | sed 's/^## //' | sed 's/ .*//')
      if [[ -n "$version" ]]; then
        MODULE_VERSIONS["$dir"]="$version"
        echo "  $dir -> v$version"
      fi
    fi
  fi
done < <(grep '^\s*\./' go.work | sed 's/^[[:space:]]*//')

if [[ ${#MODULE_VERSIONS[@]} -eq 0 ]]; then
  echo "No modules to release."
  exit 0
fi

# --- Step 2: Build dependency graph and compute levels ---

echo ""
echo "=== Computing dependency levels ==="

declare -A MODULE_LEVEL     # dir -> level
declare -A MODULE_INTERNAL_DEPS  # dir -> space-separated list of internal dep dirs

# For each module, find its internal dependencies
for dir in "${!MODULE_VERSIONS[@]}"; do
  deps=""
  while IFS= read -r dep_mod; do
    dep_dir="${MODULE_DIRS[$dep_mod]:-}"
    if [[ -n "$dep_dir" ]]; then
      deps="$deps $dep_dir"
    fi
  done < <(grep "$MODULE_PREFIX" "$dir/go.mod" 2>/dev/null | grep -v '^module' | awk '{print $1}' | sort -u)
  MODULE_INTERNAL_DEPS["$dir"]="${deps# }"
done

# Topological sort into levels
# Level 0: no internal deps (or deps not being released)
# Level N: all deps are in levels < N
max_iterations=20
for (( iteration=0; iteration<max_iterations; iteration++ )); do
  progress=false
  for dir in "${!MODULE_VERSIONS[@]}"; do
    # Skip already assigned
    if [[ -n "${MODULE_LEVEL[$dir]:-}" ]]; then continue; fi

    # Check if all deps have been assigned a level
    all_deps_resolved=true
    max_dep_level=-1
    for dep_dir in ${MODULE_INTERNAL_DEPS[$dir]:-}; do
      dep_level="${MODULE_LEVEL[$dep_dir]:-}"
      if [[ -z "$dep_level" ]]; then
        # Dep not being released — treat as already published (level -1)
        if [[ -z "${MODULE_VERSIONS[$dep_dir]:-}" ]]; then
          continue
        fi
        all_deps_resolved=false
        break
      fi
      if (( dep_level > max_dep_level )); then
        max_dep_level=$dep_level
      fi
    done

    if $all_deps_resolved; then
      MODULE_LEVEL["$dir"]=$(( max_dep_level + 1 ))
      progress=true
    fi
  done

  if ! $progress; then
    # Check if all modules are assigned
    all_done=true
    for dir in "${!MODULE_VERSIONS[@]}"; do
      if [[ -z "${MODULE_LEVEL[$dir]:-}" ]]; then
        all_done=false
        echo "  WARNING: Could not assign level to $dir (circular dep?), assigning level $iteration"
        MODULE_LEVEL["$dir"]=$iteration
      fi
    done
    if $all_done; then break; fi
  fi
done

# Find max level
max_level=0
for dir in "${!MODULE_LEVEL[@]}"; do
  level="${MODULE_LEVEL[$dir]}"
  if (( level > max_level )); then max_level=$level; fi
done

# Print levels
for (( level=0; level<=max_level; level++ )); do
  modules_at_level=""
  for dir in "${!MODULE_LEVEL[@]}"; do
    if [[ "${MODULE_LEVEL[$dir]}" -eq $level ]]; then
      modules_at_level="$modules_at_level $dir"
    fi
  done
  echo "  Level $level:$modules_at_level"
done

# --- Step 3: Pre-tag gate ---
# nx release version must have rewritten every internal require to a
# real version before we tag. If anything still pins an internal
# rho-kit module at v0.0.0, abort before producing a broken tag —
# downstream consumers do not inherit the workspace's local replaces.
echo ""
echo "=== Pre-tag gate: no internal v0.0.0 requires ==="
bash "$(dirname "$0")/check-no-internal-v0.sh"

# --- Step 4: Release each level ---

for (( level=0; level<=max_level; level++ )); do
  echo ""
  echo "=== Releasing level $level ==="

  # Collect modules at this level
  level_modules=()
  for dir in "${!MODULE_LEVEL[@]}"; do
    if [[ "${MODULE_LEVEL[$dir]}" -eq $level ]]; then
      level_modules+=("$dir")
    fi
  done

  # Run go mod tidy for each module at this level
  for dir in "${level_modules[@]}"; do
    echo "  go mod tidy: $dir"
    (cd "$dir" && GOWORK=off go mod tidy)
  done

  # Stage files for this level
  for dir in "${level_modules[@]}"; do
    git add "$dir/go.mod" "$dir/go.sum" "$dir/CHANGES.md" 2>/dev/null || true
  done

  # Downstream-consumer validation:
  # `replace` directives in a published go.mod are ignored by Go consumers
  # (they only apply to the main module). Local replaces in this monorepo
  # mask broken require versions during workspace tidy. Verify each module
  # at this level resolves its dependency graph with local replace directives
  # temporarily removed — the same view a downstream consumer sees.
  for dir in "${level_modules[@]}"; do
    has_local_replace=$(awk '
      /^replace[[:space:]]*\(/ { in_block=1; next }
      in_block && /^\)/ { in_block=0; next }
      in_block && /=>[[:space:]]*\.\.?\// { print "y"; exit }
      /^replace[[:space:]].*=>[[:space:]]*\.\.?\// { print "y"; exit }
    ' "$dir/go.mod")
    if [[ -z "$has_local_replace" ]]; then
      continue
    fi
    echo "  validating downstream resolution: $dir (replaces stripped)"
    cp "$dir/go.mod" "$dir/go.mod.release-bak"
    cp "$dir/go.sum" "$dir/go.sum.release-bak"
    awk '
      BEGIN { skip=0 }
      /^replace[[:space:]]*\(/ { skip=1; next }
      skip && /^\)/ { skip=0; next }
      skip { next }
      /^replace[[:space:]].*=>[[:space:]]*\.\.?\// { next }
      { print }
    ' "$dir/go.mod.release-bak" > "$dir/go.mod"
    if ! (cd "$dir" && GOWORK=off go list -mod=mod ./... > /dev/null); then
      echo "  ERROR: $dir cannot resolve dependencies without local replaces."
      echo "  This module would be broken for downstream consumers when published."
      mv "$dir/go.mod.release-bak" "$dir/go.mod"
      mv "$dir/go.sum.release-bak" "$dir/go.sum"
      exit 1
    fi
    mv "$dir/go.mod.release-bak" "$dir/go.mod"
    mv "$dir/go.sum.release-bak" "$dir/go.sum"
  done

  # Build commit message and tag list
  tags=()
  commit_parts=()
  for dir in "${level_modules[@]}"; do
    version="${MODULE_VERSIONS[$dir]}"
    tag="${dir}/v${version}"
    tags+=("$tag")
    commit_parts+=("${dir}/v${version}")
  done

  # Commit
  commit_msg="chore(release): $(IFS=', '; echo "${commit_parts[*]}")"
  if git diff --cached --quiet; then
    echo "  No changes to commit at level $level, skipping"
    # Still need to create tags pointing to current HEAD
    for tag in "${tags[@]}"; do
      echo "  Tagging: $tag"
      git tag -f "$tag"
    done
  else
    git commit -m "$commit_msg"
    for tag in "${tags[@]}"; do
      echo "  Tagging: $tag"
      git tag "$tag"
    done
  fi

  # Push commit + tags
  echo "  Pushing level $level..."
  git push origin main
  for tag in "${tags[@]}"; do
    git push origin "$tag"
  done

  echo "  Level $level released."
done

echo ""
echo "=== Release complete ==="
