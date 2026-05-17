// Package circuitbreaker provides a simple three-state circuit breaker.
//
// It wraps github.com/sony/gobreaker with custom defaults and a small surface:
// Execute runs a call, ErrCircuitOpen indicates short-circuiting, and
// WithPermanentSuccess avoids tripping on apperror.Permanent failures.
//
// Observability:
//   - WithMetrics wires built-in Prometheus counters
//     (circuitbreaker_state_changes_total, circuitbreaker_calls_total)
//     so consumers don't have to hand-roll OnStateChange callbacks just
//     to populate dashboards. WithOnStateChange remains the right tool
//     for service-specific reactions (paging, audit log writes, etc.)
//     and runs AFTER the metric record so a panicking callback cannot
//     suppress the counter.
//   - The wave-167 OTel tracing emits a span per Execute/ExecuteCtx;
//     metrics and traces are complementary.
package circuitbreaker
