# v2.0.0 benchmark baselines

> **STATUS (2026-05-14): HISTORICAL / PRELIMINARY — NOT canonical
> `kit-bench-gate -baseline` input for the v2.0.0 release tag.**
>
> These files were captured on 2026-05-12 at revision
> `5fb930d7ac5b206b6174be535fc4751a6c33d6b2`, before the wave 37–57
> source / API churn (approval / outbox / auditlog defaults,
> ctx+error key-store / resolver sweep, Prometheus contract
> standardisation, NATS credential bridge, AMQP / NATS metric
> label-default flip, TLS reload wiring, broker TLS reload propagation,
> MCP destructive gate,
> auditlog append-order RangeChain, idempotency `WithRequiredMethods`
> safety, memory-store ctx checks). The rows reflect symbols that
> have since been renamed (`BenchmarkWithIDChecked` → `BenchmarkWithID`)
> or removed entirely (`httpx/reqsign`).
>
> Treat the existing files as the closest available historical
> comparison points for the unaffected hot paths only. Before
> tagging, rerun `make bench-baseline` from a clean release-candidate
> commit on release-candidate hardware and replace this directory
> wholesale; the resulting MANIFEST will drop this notice.

Generated (preliminary): `2026-05-12T14:32:57Z`

- Source Git revision (preliminary capture): `5fb930d7ac5b206b6174be535fc4751a6c33d6b2`
- Source working tree at capture: `clean`
- Go: `go version go1.26.3 darwin/arm64`
- GOOS/GOARCH: `darwin/arm64`
- Command shape: `go test -run=^$ -bench=. -benchmem -count=5 ./...`

Source metadata is captured before benchmark output files are
rewritten; the benchmark output directory is ignored when computing
source-tree cleanliness.

## Captured Modules

- `core` -> `core.bench`
- `crypto` -> `crypto.bench`
- `data` -> `data.bench`
- `httpx` -> `httpx.bench`
- `resilience` -> `resilience.bench`
- `runtime` -> `runtime.bench`

## Known stale rows

- `httpx.bench` includes baselines for `httpx/reqsign`, which was
  removed in v2.0.0 ahead of the tag. Those rows reflect the v1
  baseline retained for historical reference; new bench runs will
  omit them. Use `httpx/sign` with `httpx/middleware/signedrequest`
  as the active signed-request path.
- `core.bench` was captured before the `WithIDChecked` removal:
  `tenant.WithID` was renamed and the standalone `WithIDChecked`
  helper was deleted. The `BenchmarkWithIDChecked-16` rows in
  `core.bench` therefore reflect the pre-rename baseline; the
  post-rename benchmark is `BenchmarkWithID` and a refresh will
  replace those rows.
