// Package circuitbreaker provides a simple three-state circuit breaker.
//
// It wraps github.com/sony/gobreaker with custom defaults and a small surface:
// Execute runs a call, ErrCircuitOpen indicates short-circuiting, and
// WithPermanentSuccess avoids tripping on apperror.Permanent failures.
// Use WithOnStateChange to emit metrics or logs when the breaker opens/closes.
package circuitbreaker
