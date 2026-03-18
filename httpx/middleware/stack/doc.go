// Package stack provides a canonical HTTP middleware chain.
//
// Default applies metrics, request IDs, tracing, and logging in the recommended
// order, with options to disable or extend the chain. Outer middleware wraps
// the full stack, while Inner middleware runs closest to the handler.
package stack
