# NEW: observability/redmetrics

**Phase**: 3 (DX)
**Module path**: `github.com/bds421/rho-kit/observability/redmetrics`

## Why

`observability/promutil` provides primitives but not opinionated middleware. Every service ends up writing similar Rate/Errors/Duration counters and histograms with slightly different labels and (often wrong) histogram buckets.

Ship a constructor that produces the standard RED set with sensible buckets.

## Public API

```go
package redmetrics

// HTTPMetrics builds the standard set of HTTP RED metrics.
type HTTPMetrics struct {
    Requests  *prometheus.CounterVec   // labels: route, method, status
    Errors    *prometheus.CounterVec   // labels: route, method, status_class
    Duration  *prometheus.HistogramVec // labels: route, method
    InFlight  prometheus.Gauge
}

// NewHTTP registers HTTP RED metrics with the given registerer.
// Default duration buckets: 5ms, 10ms, 25ms, 50ms, 100ms, 250ms, 500ms,
// 1s, 2.5s, 5s, 10s, 30s. Tuned for typical HTTP latency.
func NewHTTP(reg prometheus.Registerer, opts ...Option) *HTTPMetrics

// Middleware returns http middleware that records all four metrics.
// Route label comes from a route extractor (default: r.URL.Path with parameter
// templating from chi/gorilla mux if available, else "unknown" — explicit is
// safer than high-cardinality).
func (m *HTTPMetrics) Middleware(routeExtractor func(*http.Request) string) func(http.Handler) http.Handler
```

```go
// Equivalent for gRPC:
type GRPCMetrics struct { ... }
func NewGRPC(reg prometheus.Registerer, opts ...Option) *GRPCMetrics
func (m *GRPCMetrics) UnaryInterceptor() grpc.UnaryServerInterceptor
func (m *GRPCMetrics) StreamInterceptor() grpc.StreamServerInterceptor
```

```go
// Equivalent for batch / cron with wider buckets:
type BatchMetrics struct { ... }
func NewBatch(reg prometheus.Registerer, name string, opts ...Option) *BatchMetrics
// Default buckets: 0.1s, 1s, 5s, 10s, 30s, 60s, 120s, 300s, 600s, 1800s, 3600s.
```

## Replaces

- The existing `httpx/middleware/metrics` becomes a thin wrapper over `redmetrics.NewHTTP(...)` for backward compat.
- `runtime/batchworker/metrics.go` and `runtime/cron/metrics.go` use `redmetrics.NewBatch` for proper buckets (closes the audit finding).

## Definition of done

- [ ] HTTP, gRPC, Batch metric constructors with proper buckets.
- [ ] Middleware/interceptor wiring.
- [ ] Existing httpx/middleware/metrics refactored to delegate.
- [ ] batchworker + cron use the Batch constructor.
- [ ] Recipe in `docs/ai/utilities.md`.
