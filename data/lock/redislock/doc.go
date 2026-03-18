// Package lock provides distributed mutual exclusion using Redis.
//
// # Limitations
//
// This is an advisory lock backed by a single Redis instance using SET NX
// with a TTL. It is suitable for leader election, deduplication, and
// coordination where occasional double-execution is acceptable. It is NOT
// suitable for correctness-critical mutual exclusion because:
//
//   - No fencing tokens: a slow holder that outlives the TTL cannot detect
//     that another process has since acquired the lock.
//   - Single-node: if Redis restarts, all locks are lost. Redlock is not
//     implemented.
//   - Clock-dependent: TTL accuracy depends on Redis and client clocks
//     agreeing within reasonable bounds.
//
// For safety-critical locking (e.g., preventing double-spend), use a
// database-level advisory lock (SELECT ... FOR UPDATE) or a consensus
// system (etcd, ZooKeeper).
package redislock
