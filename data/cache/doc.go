// Package cache defines a generic cache interface and in-memory implementation.
//
// The MemoryCache uses Ristretto with TTL support and returns copies of values
// to keep callers from mutating cached data. It is suitable as a local L1 cache
// or for tests; production multi-instance caching should use Redis.
//
// # ComputeCache observability
//
// ComputeCache exposes Prometheus metrics when constructed with
// WithComputePrometheusMetrics or WithComputeMetricsRegisterer. All metrics
// share the cache_compute_* prefix and the "name" label set by
// WithComputeName (default "default").
//
//   - cache_compute_hits_total{name}                    fresh cache hits.
//   - cache_compute_misses_total{name}                  compute triggered.
//   - cache_compute_stale_serves_total{name}            served stale while refreshing.
//   - cache_compute_errors_total{name}                  backend or compute errors.
//   - cache_compute_singleflight_inflight{name}         current in-flight leaders.
//   - cache_compute_singleflight_wait_seconds{name}     follower wait time histogram.
//   - cache_compute_singleflight_followers_total{name}  callers that joined a leader.
//
// The singleflight metrics make thundering-herd behaviour visible:
// high followers_total with low inflight indicates dedup is working as
// designed; high inflight indicates compute latency is dominating the
// cache layer.
package cache
