# NEW: observability/pprof + observability/runtimemetrics

**Phase**: 3 (DX)
**Module path**: `github.com/bds421/rho-kit/observability/pprof` and `.../runtimemetrics`

## Why

The kit serves `/ready` and `/metrics` on the internal port `:9090`. Operators need two more things on the same port:

1. **`/debug/pprof/*`** — heap, goroutines, mutex, block profiles. Today consumers add this manually (and often forget the auth gate).
2. **Go-runtime metrics** — heap, goroutine count, GC pauses, MaxRSS. These are available from `runtime/metrics` but aren't exported to Prometheus by default.

Two small packages, both wired by the Builder.

## Public APIs

```go
package pprof

// Handler returns an http.Handler with /debug/pprof/* routes mounted.
// Must be wrapped with auth before exposing externally.
func Handler() http.Handler

// Mount installs pprof routes on the given mux. Pass auth wrappers via opts.
func Mount(mux *http.ServeMux, opts ...Option)
```

```go
package runtimemetrics

// Register registers Go runtime metrics with the given Prometheus registerer.
// Includes:
//   - go_gc_pause_seconds (histogram)
//   - go_goroutines (gauge)
//   - go_heap_bytes (gauge)
//   - go_threads (gauge)
//   - go_max_rss_bytes (gauge, where supported)
func Register(reg prometheus.Registerer)
```

## Builder integration

```go
// Already-existing internal :9090 server gains:
//   /metrics      (existing)
//   /ready        (existing)
//   /live         (new — see existing/16-observability.md)
//   /debug/pprof/ (new, gated on env != production OR opt-in flag)
```

`Builder.WithPprof()` enables the routes. By default, `pprof` is only enabled when `env != production`. Document the security implications.

## Definition of done

- [ ] `pprof` package with Handler + Mount.
- [ ] `runtimemetrics` package + Register.
- [ ] Builder `WithPprof()` (off by default in production).
- [ ] Internal mux registers `/debug/pprof/` when enabled.
- [ ] Internal mux exposes runtime metrics by default (cheap; no env gate).
- [ ] Recipe entry in `docs/ai/utilities.md`.
