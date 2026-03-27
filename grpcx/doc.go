// Package grpcx provides production-ready gRPC server construction with
// interceptors for observability, auth, and resilience.
//
// It mirrors the patterns established by httpx: safe defaults, functional
// options, and Prometheus/OTel integration. Use NewServer to create a server
// with sensible defaults, or compose individual interceptors for custom setups.
//
// Health bridge: HealthServer bridges the kit's health.Checker to the standard
// gRPC Health Checking Protocol (grpc.health.v1), allowing Kubernetes gRPC
// probes and load balancers to query service readiness.
//
// Interceptor chain order (innermost to outermost):
//
//	recovery → metrics → logging → auth → tracing → handler
//
// This ensures panics are caught before metrics record, and auth runs after
// tracing so denied requests still get spans for debugging.
package grpcx
