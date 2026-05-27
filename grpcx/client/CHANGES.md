# Changes

## Unreleased — v2.0

- Initial release of the symmetric client surface for `grpcx`.
- `NewClient(target, opts...)` with kit-hardened defaults: TLS (or
  loopback insecure), default 30s deadline, keepalive, chained recovery
  + logging + metrics interceptors, optional retry.
- `grpcx/client/interceptor` exposes the client-side recovery, deadline,
  logging, metrics, and retry interceptors for callers building custom
  chains.
- Metrics registered as `grpc_client_handled_total` and
  `grpc_client_handling_seconds` (distinguished from server-side
  `grpc_server_*`).
