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
//   - Auth: JWT extraction from gRPC metadata (AuthUnary, AuthStream)
//   - mTLS S2S: JWT or verified mTLS + SAN/CN allowlist (MTLSAuthUnary, MTLSAuthStream)
//   - RBAC: per-method permission and scope checks (RequirePermissionUnary,
//     RequirePermissionStream, RequireScopeUnary, RequireScopeStream)
//
// # Authorization primitives
//
// The kit ships gRPC equivalents of the HTTP authorization primitives in
// httpx/middleware/auth. The composition is:
//
//  1. Authentication interceptor — AuthUnary (JWT only) or MTLSAuthUnary
//     (JWT or mTLS S2S). Populates ctx with userID + permissions + scopes.
//     MTLSAuthUnary additionally stamps a trusted-S2S marker on the mTLS
//     branch so verified internal callers bypass RBAC without conflating
//     "verified service" with "JWT happened to lack a permissions claim".
//  2. RequirePermissionUnary / RequireScopeUnary — applied per-method (or
//     to all methods via grpc.ChainUnaryInterceptor). These fail closed:
//     a request without the trusted-S2S marker AND without a matching
//     permission/scope is rejected with codes.PermissionDenied.
//  3. The handler.
//
// The recommended order for ChainUnaryInterceptor is: Recovery, Logging,
// Metrics, MTLSAuthUnary (or AuthUnary), then any RequirePermissionUnary /
// RequireScopeUnary, then handler-specific interceptors.
//
// Use [IsTrustedS2S] inside a handler to differentiate verified internal
// callers (skip per-tenant rate limit, log under a different actor, etc.)
// from end-user requests.
//
// Skip-method allowlists (WithSkipMethods) bypass authentication entirely
// for the listed gRPC methods (e.g. /grpc.health.v1.Health/Check). Skip
// methods do NOT bypass RequirePermission / RequireScope; if you skip auth
// for a method you must also skip the per-method authorization
// interceptor for it (or rely on it not being chained for that method).
//
// For OpenTelemetry tracing, use grpcx.WithTracingStatsHandler which leverages
// the otelgrpc stats handler API (preferred over deprecated interceptors).
package interceptor
