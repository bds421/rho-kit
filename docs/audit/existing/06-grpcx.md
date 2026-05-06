# grpcx/ — gRPC server, client, interceptors

## Landed

- ✅ **`grpcx.NewServer` installs Recovery interceptors by default** — `RecoveryUnary` and `RecoveryStream` are prepended to the chain so they wrap every other interceptor and the handler itself; a panic anywhere converts to `codes.Internal` with a structured log entry rather than a silent connection teardown. `WithoutRecovery` opts out for tests that need to assert raw panic propagation; `WithRecoveryLogger` overrides the slog.Default() recovery logger (commit `e96ffdf`). Closes the original CRITICAL #3.

## Open

_(All originally-flagged grpcx items closed.)_

### Migration checklist

- [x] Phase 1: prepend Recovery interceptors in `NewServer`; add opt-out option for tests. ✅ `e96ffdf`
- [x] Phase 1: same in `app.NewGRPCModule` — module already calls `NewServer`, so it inherits the default automatically.
