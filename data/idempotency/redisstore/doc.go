// Package idempotencystore provides a Redis-backed implementation of the
// idempotency.Store interface for multi-instance deployments. Use this
// instead of idempotency.NewMemoryStore in production.
package redisstore
