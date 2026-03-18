// Package middleware contains shared HTTP middleware utilities.
//
// Services should prefer stack.Default for the canonical ordering, then layer
// feature-specific middleware (auth, CSRF, rate limit) as needed.
package middleware
