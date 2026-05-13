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
// Recommended interceptor chain order (outermost to innermost):
//
//	recovery → metrics → logging → auth → handler
//
// This ensures recovery catches panics from all subsequent interceptors,
// metrics record every call (including auth failures), and auth runs after
// logging so denied requests are still logged.
//
// For tracing, use WithTracingStatsHandler (stats handler API) rather than
// interceptors — it captures both unary and streaming RPCs automatically.
//
// # Streaming exhaustion guard
//
// Go's gRPC default for MaxConcurrentStreams is math.MaxUint32, leaving
// a single TCP peer free to open ~4B HTTP/2 streams against the server.
// Each stream pins a goroutine and stream-state memory until the per-RPC
// deadline fires — the GAP-03 vector in docs/audit/THREAT_MODEL.md §4.2.
// NewServer pins the limit to 1000 streams per connection by default;
// operators with legitimate fan-in proxies can raise it via
// [WithMaxConcurrentStreams].
package grpcx
