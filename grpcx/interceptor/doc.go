// Package interceptor provides gRPC server interceptors for observability,
// authentication, and resilience.
//
// Each interceptor follows the same pattern as httpx/middleware: a constructor
// returns a grpc.UnaryServerInterceptor (and optionally a
// grpc.StreamServerInterceptor) that can be composed via grpcx.NewServer.
//
// Available interceptors:
//   - Recovery: catches panics and returns codes.Internal
//   - Metrics: records grpc_server_handled_total and grpc_server_handling_seconds
//   - Logging: structured request logging with correlation ID
//   - Auth: JWT extraction from gRPC metadata
//
// For OpenTelemetry tracing, use grpcx.WithTracingStatsHandler which leverages
// the otelgrpc stats handler API (preferred over deprecated interceptors).
package interceptor
