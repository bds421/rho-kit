// Package circuitbreaker wraps a [storage.Storage] backend with a circuit
// breaker that prevents cascading failures when the backend is unavailable.
//
// Internally delegates to [kit/circuitbreaker.CircuitBreaker] (sony/gobreaker),
// avoiding a duplicated state machine implementation.
//
// States:
//   - Closed: normal operation, all calls pass through.
//   - Open: all calls fail immediately with [ErrCircuitOpen].
//   - HalfOpen: one probe call is allowed; success closes, failure re-opens.
//
// Usage:
//
//	cb := circuitbreaker.New(s3Backend,
//	    circuitbreaker.WithThreshold(5),
//	    circuitbreaker.WithResetTimeout(30*time.Second),
//	)
//	err := cb.Put(ctx, key, reader, meta) // fails fast when circuit is open
package circuitbreaker
