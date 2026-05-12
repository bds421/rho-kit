# v2.0.0 benchmark baselines

Generated: `2026-05-12T14:24:46Z`

- Git revision: `4f2c48f771ac0cf6b33f6eaf3bf16762aafec92d`
- Working tree: `clean`
- Go: `go version go1.26.3 darwin/arm64`
- GOOS/GOARCH: `darwin/arm64`
- Command shape: `go test -run=^$ -bench=. -benchmem -count=5 ./...`

These files are intended as `kit-bench-gate -baseline` inputs. Refresh
them on release-candidate hardware before tagging if the machine changes.

## Captured Modules

- `core` -> `core.bench`
- `crypto` -> `crypto.bench`
- `data` -> `data.bench`
- `httpx` -> `httpx.bench`
- `resilience` -> `resilience.bench`
- `runtime` -> `runtime.bench`
