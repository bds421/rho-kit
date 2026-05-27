# grpcx/client

## Purpose

gRPC client construction with the same opinionated defaults the kit
applies on the server side: TLS-only (insecure rejected for non-loopback),
default per-RPC deadline, keepalive, chained recovery/logging/metrics
interceptors, optional retry on UNAVAILABLE / RESOURCE_EXHAUSTED / ABORTED.

## Public API

- `NewClient(target string, opts ...Option) (*grpc.ClientConn, error)`
- Options: `WithTLSConfig`, `WithInsecure` (loopback only), `WithDefaultTimeout`,
  `WithoutDefaultDeadline`, `WithoutRecovery`, `WithRecoveryLogger`,
  `WithLogger`, `WithoutLogging`, `WithMetricsRegisterer`, `WithoutMetrics`,
  `WithRetry`, `WithRetryableCodes`, `WithUnaryInterceptors`,
  `WithStreamInterceptors`, `WithDialOptions`, `WithKeepaliveParams`

## Interceptor chain (outermost → handler)

```
recovery -> logging -> metrics -> retry (optional) -> deadline -> caller -> RPC
```

Each layer has a documented opt-out (`WithoutRecovery`, `WithoutLogging`,
`WithoutMetrics`, `WithoutDefaultDeadline`).

## Operability

- TLS `MinVersion` is floored to TLS 1.2 via `tlsclone.ConfigWithFloor`;
  `InsecureSkipVerify=true` panics inside the floor helper.
- `WithInsecure` panics if `target` is not a loopback address.
- **Keepalive is non-overridable from caller `DialOptions`.** The kit
  appends `grpc.WithKeepaliveParams(defaults)` AFTER any caller-supplied
  `WithDialOptions(...)`, and gRPC's last-writer-wins for non-additive
  setters means the kit defaults win. Mirrors `grpcx.NewServer`'s
  hardening pattern (see `grpcx/server.go` final-append section).
  Override the keepalive intentionally via `WithKeepaliveParams(...)` —
  that knob is set INSIDE the kit-hardened block and stays effective.
- Caller-supplied `DialOptions` go AFTER kit-hardened options so callers
  can extend (service config, custom resolver) but cannot silently undo
  credentials or keepalive.
- Metrics: `grpc_client_handled_total{grpc_method, grpc_code}` and
  `grpc_client_handling_seconds{grpc_method}` — `_client_` subsystem
  distinguishes them from the server-side `grpc_server_*` family.

## Tests

`go test -race ./...` from this directory. Covers loopback insecure
dial, insecure-on-non-loopback panic, missing-credentials panic,
empty-target panic, nil-TLS-config panic, TLS floor accepted,
nil-option panic, custom metrics registerer isolation, and retry on
UNAVAILABLE (flaky test server fails twice then succeeds; retry policy
recovers the call).

## See also

- `grpcx` — server. Identical option vocabulary so a kit user writing
  both ends of an internal RPC pair never has to translate idioms.
- `grpcx/client/interceptor` — exported client interceptors for callers
  building a custom chain.
- `resilience/retry` — the underlying retry policy machinery.
