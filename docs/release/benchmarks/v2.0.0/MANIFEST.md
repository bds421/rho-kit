# v2.0.0 benchmark baselines

Generated: `2026-05-12T11:26:38Z`

- Git revision: `611edddbbd72e4f36f7c7e610865686da39818a2`
- Working tree: `dirty`
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
