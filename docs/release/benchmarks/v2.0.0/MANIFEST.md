# v2.0.0 benchmark baselines

Generated: `2026-05-12T14:32:57Z`

- Source Git revision: `5fb930d7ac5b206b6174be535fc4751a6c33d6b2`
- Source working tree: `clean`
- Go: `go version go1.26.3 darwin/arm64`
- GOOS/GOARCH: `darwin/arm64`
- Command shape: `go test -run=^$ -bench=. -benchmem -count=5 ./...`

These files are intended as `kit-bench-gate -baseline` inputs. Refresh
them on release-candidate hardware before tagging if the machine changes.

Source metadata is captured before benchmark output files are rewritten;
the benchmark output directory is ignored when computing source-tree cleanliness.

## Captured Modules

- `core` -> `core.bench`
- `crypto` -> `crypto.bench`
- `data` -> `data.bench`
- `httpx` -> `httpx.bench`
- `resilience` -> `resilience.bench`
- `runtime` -> `runtime.bench`

## Notes

- `httpx.bench` includes baselines for `httpx/reqsign`, which was removed in
  v2.0.0 ahead of the tag. Those rows reflect the v1 baseline retained for
  historical reference; new bench runs will omit them. Use `httpx/sign` with
  `httpx/middleware/signedrequest` as the active signed-request path.
