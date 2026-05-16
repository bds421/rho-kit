# AGENTS.md — `cmd/kit-catalog`

## When to use this package

- An ops/platform team running many kit-using services needs
  fleet-wide visibility: which services use a specific kit
  package, which versions are pinned where, who's on the
  oldest module.
- A kit CVE drops and the team needs "which services include
  the affected package" within minutes — `kit-catalog -fleet
  ... -uses <pkg>` answers exactly that.
- A backend migration (Postgres idempotency → Redis idempotency,
  in-memory broker → real broker) needs an inventory of which
  services currently compose which adapter.

## When to use something else

- **Single-service composition view:** `go list -m all` in
  that one service. `kit-catalog` (no `-fleet`) does work for
  one service too, but `go list` is more idiomatic when you're
  already inside the service tree.
- **Security-rule audit (not composition inventory):**
  `cmd/kit-doctor` flags misuse patterns. `kit-catalog`
  inventories WHAT'S USED; `kit-doctor` audits HOW IT'S USED.
- **Cross-language fleet inventory:** out of scope. The tool is
  Go-only and reads `go.mod` directly.

## Key APIs (CLI flags)

- `-fleet <dir>` — Scan every immediate subdirectory of `<dir>`
  that contains a `go.mod`. Without this flag, scans `cwd` as a
  single service.
- `-uses <import path>` — Filter manifest to services that
  import the exact path (e.g. `github.com/bds421/rho-kit/data/v2/idempotency/pgstore`).
- `-format json|table|csv` — Output format. Default `json` for
  pipelines, `table` for terminal, `csv` for spreadsheet
  imports.

## Output shape (JSON)

```json
{
  "scanned_at":    "2026-05-16T12:34:56Z",
  "service_count": 12,
  "services": [
    {
      "module":       "github.com/example/orders-api",
      "path":         "/srv/orders-api",
      "kit_packages": [
        "github.com/bds421/rho-kit/httpx/v2",
        "github.com/bds421/rho-kit/data/v2/idempotency/pgstore"
      ],
      "kit_versions": {
        "github.com/bds421/rho-kit/httpx/v2": "v2.0.3"
      }
    }
  ]
}
```

## Common mistakes

- **Pointing `-fleet` at a directory tree, not the parent of
  service directories.** The flag scans the IMMEDIATE
  subdirectories of `<dir>`. For nested fleets, run the tool
  per layer or write a shell wrapper.
- **Expecting test imports to count.** Test files are skipped
  on purpose — the manifest reflects PRODUCTION composition.
  This means a service that only uses `testing/kittest` in
  `_test.go` will not show that import in the manifest.
- **Reading the kit_versions map as a complete go.sum.** The
  map is filtered to the modules the service actually imports
  in non-test code — indirect deps drop out. For a complete
  pin list, read `go.sum` directly.
- **Treating the inventory as authoritative for unused kit
  modules.** A service can list a kit module in `require` but
  never import it in non-test code (legacy bloat). The
  `kit_versions` map will omit such entries; the `go.mod` row
  is still there if you grep.

## Observability

- No metrics: the tool is a one-shot CLI, not a long-running
  process.
- Stdout: JSON manifest (or table / CSV) on success.
- Exit codes:
  - 0 — manifest emitted
  - 1 — no services matched (filter or empty fleet)
  - 2 — CLI / discovery error
