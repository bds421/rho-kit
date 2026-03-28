Read and follow AGENTS.md in this repository root — it is the canonical AI agent guide for this project, including build commands, package decision tree, golden path, conventions, and links to detailed recipe files in docs/ai/.

## Multi-Module Release Rule (CRITICAL)

This repo uses release-please with independent Go modules. release-please tags all modules in a single release PR — it cannot do phased releases.

**Rule: A PR must NEVER introduce new usage of a sibling module's unreleased features.**

If module A (e.g., `httpx`) needs a new type/function from module B (e.g., `core/apperror`), these MUST be in separate release cycles:
1. PR 1: Add the new feature to module B → merge → release-please tags B's new version
2. PR 2: Bump B's version pin in A's `go.mod` + use the new feature in A → merge → release-please tags A

Combining both in one release creates a broken tag: A's `go.mod` pins B at the old version but uses features that only exist in B's new version. `go get` will fail for consumers.

**When reviewing PRs:** If a PR modifies multiple modules AND one module's `go.mod` depends on the other, verify the dependency is already published at the required version. If not, split the PR.

## PR Title Convention (CRITICAL for release-please)

release-please only creates releases for `feat:` and `fix:` prefixed commits. Other prefixes (`refactor:`, `chore:`, `docs:`, `test:`) are ignored.

**Rule: If a PR adds, removes, or changes public API, use `feat:` — even if it also refactors.**

- Adding new types/functions → `feat:`
- Removing/renaming public API → `feat:` (it's a feature change, not just cleanup)
- Bug fixes → `fix:`
- Internal refactoring with NO public API change → `refactor:` (won't trigger release, which is correct)

Using `refactor:` for a PR that adds new public API means it won't be tagged, breaking downstream consumers who depend on it.
