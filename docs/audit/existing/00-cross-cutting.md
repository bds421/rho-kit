# Cross-cutting findings — build tooling and convention drift

Findings that don't sit inside one package but apply across the whole repo.

## Landed

- ✅ **gRPC bump to v1.79.3** across all modules (commit `56bf04e`).
- ✅ **`make lint` sequential** — fixes golangci-lint v2 cache-lock collision (commit `56bf04e`).
- ✅ **Go runtime bump to 1.26.2** — `go.work` + all 55 module `go.mod` files; toolchain directive auto-fetches via `GOTOOLCHAIN=auto` (commit `5df122f`). Closes the 11 reachable stdlib CVEs from `make vulncheck`.
- ✅ **Nil-dependency sweep** — seven constructors (`authz.RequirePermission`, `cache.NewTypedCache`, `cache.NewComputeCache`, `pgstore.New`, `outbox.NewWriter`, `outbox.NewRelay`, `messaging.NewBufferedPublisher`) now panic / error at construction instead of deferring the panic to first request. Logger nil falls back to `slog.Default()` since dropping log lines is recoverable (commit `6ba1e7d`).

## Open

_(Cross-cutting items resolved as of Wave 4 + 5. Remaining cross-package work tracked in the per-area files.)_

### Verification status (snapshot)

From the parallel sequential audit:

| Check | Result |
|---|---|
| `make test` | Pass |
| `make test-race` | Pass |
| `make vet` | Pass |
| `make build` | Pass |
| `make lint` (sequential) | Pass |
| `make vulncheck` | **Pass (Go 1.26.2 toolchain auto-fetched)** |

`make test-cover`, `make bench`, and integration tests (with Docker) were not run.
