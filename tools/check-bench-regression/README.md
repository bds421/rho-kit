# tools/check-bench-regression

Per-hot-path benchmark regression gate. Compares the kit's most-
amplified-cost helpers (redact.WrapError,
promutil.OpaqueLabelValue, promutil.ValidateStaticLabelValue,
websocket.connLimiter) against a checked-in baseline and fails
when ns/op exceeds the baseline by more than the configured
tolerance (default 25%).

## Why this exists

Some kit helpers are called on essentially every request. A 50ns
regression in `redact.WrapError` looks invisible in isolation
but compounds across thousands of calls per request and across
millions of requests per day. The benchmark gate exists so the
v2.0.0 tag is also a performance commitment.

## Workflow

Local:

```bash
make check-bench-regression
```

The gate runs once with `-count 3` and picks the lowest ns/op per
benchmark — multi-run averaging is left for the operator's local
workflow. Lower-bound comparison is intentional: it tolerates
load-noise without masking genuine regressions.

When an intentional perf change moves the numbers:

```bash
make update-bench-baseline
git add tools/check-bench-regression/benchmarks-baseline.txt
git commit -m "perf: update bench baseline after <change>"
```

## CI integration

The gate is NOT wired into `make ci` by default — benchmark numbers
are inherently noisy across hardware and CI runners are rarely
dedicated machines. The intended workflow is:

- Developers run `make check-bench-regression` locally pre-PR.
- A dedicated nightly job (running on stable hardware) runs the
  full gate and posts a delta report.

If your CI infrastructure has dedicated benchmark runners, add
`check-bench-regression` to the `ci` target in the root Makefile.

## What's tracked

The packages benchmarked are intentionally narrow:

- **`core/redact`** — `WrapError` (the kit's universal error
  wrapper, called on every cross-boundary error).
- **`observability/promutil`** — `OpaqueLabelValue` (called on
  every cardinality-safe metric emission) and
  `ValidateStaticLabelValue` (called at every collector
  construction).
- **`httpx/websocket`** — `connLimiter` CAS-loop (per-connection
  hot path under contention).

Add a new benchmark by:

1. Adding a `Benchmark*` function under the target package's
   `bench_test.go` (use a sink variable to defeat dead-code
   elimination).
2. Adding the package pattern to `-pkgs` in
   `tools/check-bench-regression.sh` (or the default in
   `main.go`).
3. Running `make update-bench-baseline` to capture the initial
   number and committing the updated baseline file.

## Tolerance + thresholds

- **ns/op tolerance**: 1.25 (default). A run that exceeds
  `baseline * 1.25` fails.
- **allocs/op**: fails when both `current > baseline * 2` and
  `current > baseline + 1`. The "+1" guard prevents 0→1 allocs
  from flagging when the absolute number is still tiny.
- **bytes/op**: tracked in the baseline file for diagnostic value
  but not currently a fail criterion (size shifts are usually a
  derivative of alloc-count regressions).

Adjust thresholds via the flags on `check-bench-regression.sh`:

```bash
tools/check-bench-regression.sh -tolerance 1.50  # 50% allowed
tools/check-bench-regression.sh -count 5         # 5 runs, best wins
tools/check-bench-regression.sh -benchtime 2s    # longer per run
```
