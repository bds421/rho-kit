# NEW: bench-gate (CI performance regression gate)

**Phase**: 6 (Agent-readiness; complements `kit-doctor`)
**Module path**: `github.com/bds421/rho-kit/cmd/kit-bench-gate` + benchmarks per package

## Why

The kit promises "fast Go services". Without a CI gate, a PR can quietly add 50% overhead to the middleware stack and nobody notices for months. A benchmark suite + regression gate makes the "fast" promise enforceable.

This isn't about microbenchmarks for vanity — it's about catching meaningful regressions in the hot paths that every consuming service runs.

## Benchmark surface

Per-package `*_bench_test.go` files (run via `go test -bench` per module):

| Area | Benchmark | What it measures |
|---|---|---|
| `httpx/middleware/stack` | full Default chain on noop handler | Per-request overhead of the recommended stack |
| `httpx` | typed JSON handler decode/encode | JSON throughput |
| `httpx/middleware/idempotency` | hit + miss + body-fingerprint | Idempotency cost |
| `httpx/middleware/ratelimit` | hot-key contention | Limiter concurrency |
| `data/cache/rediscache` | Get/Set/MGet round-trip | Redis cache overhead |
| `data/cache` (memory) | Get/Set hot path | Memory cache cost |
| `data/cache/compute` | hit / stale-refresh / cold | Compute cache behavior |
| `data/idempotency/redisstore` | TryLock + Set | Idempotency store overhead |
| `crypto/encrypt` | Encrypt/Decrypt at typical sizes | Crypto throughput |
| `infra/messaging/amqpbackend` | publish + consume round-trip | Messaging overhead |
| `infra/storage/localbackend` | Put/Get streaming | Storage throughput |
| `runtime/concurrency` | FanOut at various widths | Goroutine pool cost |
| `runtime/eventbus` | Publish to N sync subscribers | Bus overhead |

## Gate tool

```
kit-bench-gate \
  --baseline=baseline.json \
  --current=current.json \
  --threshold=10%      # fail if any benchmark regresses by more than this
  --fail-on=ns/op,allocs/op
```

Reads `go test -bench=. -benchmem` JSON output (via `gotestsum` or `benchstat -json`), compares against a stored baseline, and fails CI if any tracked metric regresses past the threshold.

Baselines live in `bench/baselines/<package>.json` and are updated via a separate `--update-baseline` mode (gated by a label/comment on the PR).

## Workflow

```yaml
# .github/workflows/bench.yml
on: [pull_request]
jobs:
  bench:
    steps:
      - run: go test -bench=. -benchmem -count=5 ./... > current.txt
      - run: kit-bench-gate --baseline=bench/baselines --current=current.txt --threshold=10%
```

The `-count=5` plus `benchstat`-style aggregation reduces flakiness; the gate fails only on statistically significant regressions.

## What to do when it fires

A regression is *information*, not necessarily a blocker. The gate posts a comment on the PR with a benchmark diff table; the reviewer decides:
- Accept and update baseline (add a `bench/accept-regression` label).
- Block and fix.
- Investigate and ask for justification in the PR description.

## Definition of done

- [ ] Per-package benchmarks for the surface above.
- [ ] `kit-bench-gate` CLI.
- [ ] Baselines committed in `bench/baselines/`.
- [ ] GitHub Actions workflow.
- [ ] Doc explaining how to update baselines.

## Related

- [new/22-observability-dashboards.md](22-observability-dashboards.md) — bench results can also be exported to Prometheus for trend tracking.
