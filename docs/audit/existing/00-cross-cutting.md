# Cross-cutting findings — build tooling and convention drift

Findings that don't sit inside one package but apply across the whole repo. Read first because the dep-vulnerability fix gates everything else.

### [CRITICAL] Vulnerable Go runtime + grpc dep
**Source**: `make vulncheck`
**Files**: `go.work:1`; every module `go.mod` (e.g., `app/go.mod:3`, `app/go.mod:113`)
**Issue**: `make vulncheck` reports 11 reachable vulnerabilities in `app`, mostly in the Go 1.26.0 standard library (TLS, x509, URL parsing, html/template, os root handling) — fixed in Go 1.26.1/1.26.2. Also GO-2026-4762 in `google.golang.org/grpc` v1.79.2 (gRPC auth bypass), fixed in v1.79.3. All paths are reachable from service boot, HTTP serving, TLS handshakes, Redis/RabbitMQ TLS, and gRPC serving.
**Fix**: Bump the workspace and every module to Go 1.26.2+; bump `google.golang.org/grpc` to v1.79.3+ in every module that pins it directly or transitively. Then re-run `make vulncheck` across all modules.
**Effort**: M (touches every go.mod; coordinate workspace replace directives)
**Phase**: 1 (gates all other work)

### [HIGH] Constructors accept nil dependencies and fail at first use
**Files**: `httpx/authz/authz.go:38`; `data/cache/typed_cache.go:25`; `data/cache/compute.go:102`; `data/idempotency/pgstore/store.go:56`; `infra/outbox/outbox.go:88`; `infra/outbox/relay.go:90`; `infra/messaging/buffered_publisher.go:116`
**Issue**: These constructors accept nil dependencies (or empty critical inputs like nil policy/resource/subject in `authz.RequirePermission`, nil cache backend, nil SQL DB, nil outbox store/publisher/logger, nil buffered-publisher dependencies) and only fail later — by panic or nil-pointer dereference at request time. Violates the kit's own AGENTS.md anti-pattern guidance ("Fail fast: configuration errors panic at startup").
**Fix**: Add startup-time validation in each constructor + focused tests. Document the convention in CLAUDE.md / AGENTS.md so new code follows it.
**Effort**: S per constructor; M as a sweep
**Phase**: 2

### [LOW] `make lint` target unreliable (parallel runner collision)
**File**: `Makefile:13`
**Issue**: Runs `golangci-lint` concurrently across modules. golangci-lint v2 rejects parallel runners by default, so the target fails even though sequential linting reports zero issues. Sequential loop confirmed clean.
**Fix**: Run modules sequentially, set a unique cache/lock per module, or explicitly configure golangci-lint for safe parallel runners (`--concurrency` and a per-module `--cache-dir`).
**Effort**: S
**Phase**: 1

### Verification status (snapshot)

From the parallel sequential audit:

| Check | Result |
|---|---|
| `make test` | Pass |
| `make test-race` | Pass |
| `make vet` | Pass |
| `make build` | Pass |
| `make lint` (parallel) | **Fail (tooling)** |
| Sequential golangci-lint | Pass (0 issues) |
| `make vulncheck` | **Fail (deps stale)** |

`make test-cover`, `make bench`, and integration tests (with Docker) were not run.

### Migration checklist

- [ ] Phase 1: bump Go workspace + every module to 1.26.2+; bump grpc to v1.79.3+; re-run vulncheck.
- [ ] Phase 1: fix `make lint` (sequential or per-module cache).
- [ ] Phase 2: nil-dependency validation sweep across the listed constructors; document the convention.
