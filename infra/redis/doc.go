// Package redis provides resilient Redis connection management with health
// monitoring, reconnection, and Prometheus metrics.
//
// This is the root package — it contains connection, configuration, health
// checks, and command/pool metrics. Domain-specific primitives live in
// sub-packages:
//
//   - [redis/cache] — Redis-backed cache implementing the shared cache.Cache interface
//   - [redis/stream] — Redis Streams producer and consumer with consumer groups
//   - [redis/queue] — LIST-based FIFO queue with BLMOVE and dead-letter support
//
// # Connection
//
// [Connect] creates a managed Redis connection with automatic health
// monitoring, reconnection with exponential backoff and jitter, and
// lazy-connect support. Use [WithLazyConnect] for services that should
// start accepting requests while Redis is connecting.
//
// For security, the connection options (including any password) are cleared
// from memory after the client is created. Note: go-redis retains the
// password internally for its own reconnection logic — full secret erasure
// is not possible without upstream support. Use environment variables for
// password management and rotate credentials regularly.
//
// # Health Checks
//
// [HealthCheck] and [CriticalHealthCheck] integrate with
// [health.DependencyCheck] for the standard /ready endpoint.
//
// # Input Validation
//
// [ValidateName] validates resource names (streams, queues, cache names,
// instances). Names must be non-empty, at most 256 bytes, and must not
// contain null bytes, newlines, or carriage returns.
//
// # Metrics
//
// This package exports Prometheus metrics for:
//   - Command latency and errors (labeled by command, bounded by allowlist)
//   - Connection pool stats (labeled by instance)
//   - Connection health (reconnect attempts, successes, healthy gauge)
//
// Sub-packages export their own domain-specific metrics (cache hits/misses,
// stream throughput, queue depth, etc.).
//
// Important: metric labels use resource names. To avoid unbounded label
// cardinality (which can cause OOM in Prometheus), use a small, fixed set
// of static names — never embed user IDs, request IDs, or other
// high-cardinality values in resource names.
//
// # Backoff
//
// [RunWithBackoff] provides a reusable exponential backoff loop with jitter
// for sub-packages that need automatic restart on transient errors.
//
// # Error Handling
//
// Errors from this package include internal Redis key names and operation
// details for debugging. Callers should wrap these errors with
// domain-specific context before returning them to API clients — do not
// expose raw Redis errors in HTTP responses.
//
// # Panics
//
// This package panics only for programming errors detected at setup time:
//   - Empty or invalid resource names
//   - Invalid configuration options
//
// These panics fail fast to prevent silent configuration errors.
package redis
