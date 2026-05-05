# NEW: grpcx default Recovery interceptors

**Phase**: 1 (CRITICAL)
**Module path**: `github.com/bds421/rho-kit/grpcx` (modify existing `NewServer`)

This isn't a new package — it's making the existing `RecoveryUnary`/`RecoveryStream` interceptors fire by default. Listed here because it's the gRPC analog of [01-httpx-middleware-recover](01-httpx-middleware-recover.md).

## Change

```go
// Before
func NewServer(opts ...ServerOption) *grpc.Server {
    cfg := buildConfig(opts...)
    return grpc.NewServer(
        grpc.ChainUnaryInterceptor(cfg.unary...),
        grpc.ChainStreamInterceptor(cfg.stream...),
    )
}

// After
func NewServer(opts ...ServerOption) *grpc.Server {
    cfg := buildConfig(opts...)
    unary := append(
        []grpc.UnaryServerInterceptor{interceptor.RecoveryUnary(cfg.logger)},
        cfg.unary...,
    )
    stream := append(
        []grpc.StreamServerInterceptor{interceptor.RecoveryStream(cfg.logger)},
        cfg.stream...,
    )
    return grpc.NewServer(
        grpc.ChainUnaryInterceptor(unary...),
        grpc.ChainStreamInterceptor(stream...),
    )
}
```

Add `WithoutRecovery()` option for tests that want to assert panic propagation.

Apply the same change inside `app/grpc_module.go` if it constructs servers without going through `NewServer`.

## Definition of done

- [ ] `NewServer` prepends Recovery interceptors by default.
- [ ] `WithoutRecovery()` option exists.
- [ ] `app.NewGRPCModule` either calls `NewServer` or applies the same default.
- [ ] Tests: handler panic returns `codes.Internal` with structured log entry; metric incremented.
- [ ] Tests: `WithoutRecovery()` lets panics propagate (used to test panic detection).
- [ ] Doc updated.
