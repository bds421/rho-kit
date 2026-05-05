# Cross-cutting findings — build tooling and convention drift

Findings that don't sit inside one package but apply across the whole repo.

## Landed

- ✅ **gRPC bump to v1.79.3** across all modules (commit `56bf04e`).
- ✅ **`make lint` sequential** — fixes golangci-lint v2 cache-lock collision (commit `56bf04e`).

## Open

### [HIGH] Constructors accept nil dependencies and fail at first use
**Files**: `httpx/authz/authz.go:38`; `data/cache/typed_cache.go:25`; `data/cache/compute.go:102`; `data/idempotency/pgstore/store.go:56`; `infra/outbox/outbox.go:88`; `infra/outbox/relay.go:90`; `infra/messaging/buffered_publisher.go:116`
**Issue**: These constructors accept nil dependencies (or empty critical inputs like nil policy/resource/subject in `authz.RequirePermission`, nil cache backend, nil SQL DB, nil outbox store/publisher/logger, nil buffered-publisher dependencies) and only fail later — by panic or nil-pointer dereference at request time. Violates the kit's own AGENTS.md anti-pattern guidance ("Fail fast: configuration errors panic at startup").
**Fix**: Add startup-time validation in each constructor + focused tests. Document the convention in CLAUDE.md / AGENTS.md so new code follows it.
**Effort**: S per constructor; M as a sweep
**Phase**: 2

### [INFO] Go runtime bump to 1.26.2+ (operator action)
The 11 reachable stdlib CVEs reported by `make vulncheck` (TLS, x509, URL parsing, html/template, os root handling) require the operator to install Go 1.26.2+ locally. Then bump `go.work` and every `go.mod`'s `go` directive and re-run `make vulncheck`.

### Verification status (snapshot)

From the parallel sequential audit:

| Check | Result |
|---|---|
| `make test` | Pass |
| `make test-race` | Pass |
| `make vet` | Pass |
| `make build` | Pass |
| `make lint` (sequential) | Pass |
| `make vulncheck` | **Fail (Go runtime stale — operator action required)** |

`make test-cover`, `make bench`, and integration tests (with Docker) were not run.

### Migration checklist

- [ ] Phase 1: bump Go workspace + every module to 1.26.2+ once Go toolchain is upgraded; re-run vulncheck.
- [ ] Phase 2: nil-dependency validation sweep across the listed constructors; document the convention.
