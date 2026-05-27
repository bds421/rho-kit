# AGENTS.md — `grpcx`

## When to use this package

- Service exposes gRPC (unary or streaming) and wants the kit's interceptor stack: deadlines, metrics, logging, auth, recovery.
- Wants OTel tracing, Prometheus metrics, and apperror→gRPC-status mapping for free.
- Backed by `google.golang.org/grpc` (no Connect, no Twirp).

## When to use something else

- **REST/HTTP services:** `httpx` package family.
- **gRPC streaming with kit-grade resource discipline:** look at `grpcx/interceptor` for `MaxConcurrentStreamsServer` + `StreamIdleTimeout` (wave 166 hardening).

## Key APIs

- `NewServer(opts...)` — returns a configured `*grpc.Server`. Defaults: recovery + metrics + logging + apperror translation interceptors.
- `WithUnaryInterceptors(...)` / `WithStreamInterceptors(...)` — prepend custom interceptors.
- `WithDefaultTimeout(d)` — overrides `DefaultRPCDeadline` (30s) for the auto-applied per-RPC deadline interceptor (wave 119 / threat-model GAP-03 mitigation). Without a server-side cap, clients can hold streams open forever. Opt out via `WithoutDefaultDeadline()` (discouraged outside tests).

## Common mistakes

- **Disabling the default deadline in production** — `NewServer` installs the per-RPC deadline interceptor by default; `WithoutDefaultDeadline()` removes it. The threat-model GAP-03 fix landed only because every kit server should run with a per-RPC cap. Use `WithDefaultTimeout(d)` to tune the duration, not the opt-out.
- **Mixing kit interceptors with foreign apperror translators** — the kit's `apperror_status.go` converts every `apperror.*` type to the right gRPC status code. A second translator that catches anything else would compete.
- **Skipping `interceptor.MaxConcurrentStreamsServer` on streaming services** — gRPC's built-in `MaxConcurrentStreams` is per-HTTP/2-connection, not server-wide. A fleet of well-behaved clients can collectively saturate a server even when each respects the per-conn cap. Pair with `StreamIdleTimeout` for full coverage.

## Observability

- Metrics: `grpc_server_handled_total{grpc_method,grpc_code}`, `grpc_server_handling_seconds{grpc_method}`. Stream-limit additions (wave 166): `grpc_server_active_streams`, `grpc_server_streams_rejected_total{reason}`, `grpc_server_streams_idle_closed_total`.
- OTel: see `grpcx/tracing.go`. Per-call spans are emitted automatically by the metrics interceptor.
