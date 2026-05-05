# grpcx/ — gRPC server, client, interceptors

### [CRITICAL] `grpcx.NewServer` does not install Recovery interceptors by default
**File**: `grpcx/server.go:105-141` + `app/grpc_module.go:62`
**Issue**: `RecoveryUnary`/`RecoveryStream` exist in `grpcx/interceptor` but neither `NewServer` nor `app.NewGRPCModule` prepend them. A panic in a service handler returns no status to the client; gRPC default behavior is to return `UNKNOWN` and abort the stream, leaving the server in unspecified state for that RPC. The package doc promises "recovery → metrics → logging → auth" ordering — kit doesn't enforce it.
**Fix**: In `NewServer`, prepend `RecoveryUnary(slog.Default())` and `RecoveryStream(slog.Default())` to the interceptor chain. Add an opt-out option for tests. See [new/02-grpcx-recovery-default.md](../new/02-grpcx-recovery-default.md).
**Effort**: S
**Phase**: 1

### Migration checklist

- [ ] Phase 1: prepend Recovery interceptors in `NewServer`; add opt-out option for tests.
- [ ] Phase 1: do the same in `app.NewGRPCModule` if it constructs servers without going through `NewServer`.
