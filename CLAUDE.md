Read and follow AGENTS.md in this repository root — it is the canonical AI agent guide for this project, including build commands, package decision tree, golden path, conventions, and links to detailed recipe files in docs/ai/.

## NX Monorepo & Release (CRITICAL)

This repo uses **NX** with `@naxodev/gonx` for build orchestration and **NX Release** for versioning/changelogs/tagging.

### Build & CI Commands

```bash
npx nx affected -t lint       # lint only affected modules
npx nx affected -t test       # test only affected modules
npx nx affected -t build      # build only affected modules
npx nx graph                  # visualize dependency graph
npx nx run <project>:<target> # run a specific target for a project
```

NX automatically detects Go module dependencies from `go.mod` files and only runs tasks for modules affected by the current changes (plus their dependents).

### Release

Releases are handled by NX Release on the `main` branch. Each module is versioned independently using conventional commits and tagged with Go-style tags (`<module>/v<version>`).

```bash
npx nx release version --dry-run  # preview version bumps
npx nx release changelog          # generate changelogs
npx nx release publish            # create git tags and push
```

### Cross-Module PRs

NX Release with `updateDependents` handles cascading version bumps automatically. A single PR can now modify multiple interdependent modules without splitting into separate release cycles. NX will version each module independently and bump dependents as needed.

### PR Title Convention (Conventional Commits)

NX Release uses conventional commits to determine version bumps:
- `feat:` commits trigger a **minor** version bump
- `fix:` commits trigger a **patch** version bump
- Other prefixes (`refactor:`, `chore:`, `docs:`, `test:`) do **not** trigger a release

**Rule: If a PR adds, removes, or changes public API, use `feat:` — even if it also refactors.**


<!-- nx configuration start-->
<!-- Leave the start & end comments to automatically receive updates. -->

# General Guidelines for working with Nx

- For navigating/exploring the workspace, invoke the `nx-workspace` skill first - it has patterns for querying projects, targets, and dependencies
- When running tasks (for example build, lint, test, e2e, etc.), always prefer running the task through `nx` (i.e. `nx run`, `nx run-many`, `nx affected`) instead of using the underlying tooling directly
- Prefix nx commands with the workspace's package manager (e.g., `pnpm nx build`, `npm exec nx test`) - avoids using globally installed CLI
- You have access to the Nx MCP server and its tools, use them to help the user
- For Nx plugin best practices, check `node_modules/@nx/<plugin>/PLUGIN.md`. Not all plugins have this file - proceed without it if unavailable.
- NEVER guess CLI flags - always check nx_docs or `--help` first when unsure

## Scaffolding & Generators

- For scaffolding tasks (creating apps, libs, project structure, setup), ALWAYS invoke the `nx-generate` skill FIRST before exploring or calling MCP tools

## When to use nx_docs

- USE for: advanced config options, unfamiliar flags, migration guides, plugin configuration, edge cases
- DON'T USE for: basic generator syntax (`nx g @nx/react:app`), standard commands, things you already know
- The `nx-generate` skill handles generator discovery internally - don't call nx_docs just to look up generator syntax


<!-- nx configuration end-->