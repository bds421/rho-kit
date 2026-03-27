# Code Review Checklist

Standard review checklist for rho-kit PRs. Reviewers must cover ALL sections.

## 1. Correctness
- [ ] Logic is correct for all inputs (happy path + edge cases)
- [ ] Error paths are handled (not silently swallowed)
- [ ] Panic recovery where needed (Init, Close, handler callbacks)
- [ ] Resource cleanup on failure (connections, goroutines, file handles)
- [ ] No TOCTOU issues that matter (document acceptable ones)

## 2. API Ergonomics (CRITICAL — easy to miss)
- [ ] **Write a minimal example consumer** — 10 lines max to use the API. If it's more, the API needs work.
- [ ] Interface has no unnecessary methods — can an embeddable base type reduce boilerplate?
- [ ] Naming is clear without reading source — would a user understand the API from godoc alone?
- [ ] Functional options follow `With*` pattern consistently
- [ ] Types are well-chosen (`uint` vs `int`, `time.Duration` vs `float64`)
- [ ] Return types are directly usable (not raw functions requiring manual struct construction)

## 3. Package Separation
- [ ] No transport concerns in domain packages (no `net/http` in core/*, no HTTP in observability/*)
- [ ] HTTP adapters live in `httpx` or sub-packages
- [ ] New modules registered in `release-please-config.json` and `.release-please-manifest.json`
- [ ] Multi-module release rule respected (no unreleased sibling features used)

## 4. Concurrency
- [ ] Shared mutable state protected (mutex, atomic, or immutable-by-design)
- [ ] No goroutine leaks (context cancellation, cleanup on shutdown)
- [ ] Tests pass with `-race` flag
- [ ] Document thread-safety guarantees in godoc

## 5. Performance
- [ ] No allocations on hot paths (cache hits, middleware pass-through)
- [ ] Zero-cost when feature is not configured (nil checks, not empty-struct overhead)
- [ ] Pre-compute at construction time, not per-request

## 6. Security
- [ ] Untrusted input validated (headers, message fields, user-provided values)
- [ ] No panics from untrusted input — panics only for startup/config errors
- [ ] Internal details not leaked to clients (dependency names, stack traces)
- [ ] Secrets not logged

## 7. Backward Compatibility
- [ ] Existing public API unchanged (no breaking changes without major version bump)
- [ ] New features are additive (zero-value defaults preserve old behavior)
- [ ] No deprecated shims for code introduced in the same PR

## 8. Tests
- [ ] 80%+ coverage
- [ ] Edge cases tested (nil, zero, empty, negative, overflow, NaN)
- [ ] Error paths tested (not just happy path)
- [ ] No tautological assertions (test that actually verifies something)
- [ ] Tests use `-race` flag
- [ ] Integration tests use real backends where possible (miniredis, SQLite)

## 9. Code Quality
- [ ] Functions < 50 lines
- [ ] Files < 800 lines
- [ ] Immutable patterns (no mutation of shared state or input parameters)
- [ ] No hardcoded values
- [ ] No `fmt.Println` / `console.log`
- [ ] Doc comments accurate and match implementation
- [ ] No accidentally committed files (cover.out, .DS_Store)

## 10. Release Readiness
- [ ] `go.mod` version pins are for published versions
- [ ] `go.work` includes new modules
- [ ] PR description has accurate plan-vs-delivered table
- [ ] Follow-up work documented in PR description
