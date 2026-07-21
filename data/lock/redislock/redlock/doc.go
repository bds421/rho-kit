// Package redlock implements Antirez's Redlock distributed-lock
// algorithm against a quorum of independent Redis instances.
//
// # When to use this package
//
// Reach for [QuorumLocker] when your deployment requires lock
// availability across the failure of a single Redis instance — for
// example, when one node sits in each of N availability zones and a
// zone-loss must not silently release every lock. The kit's
// single-instance [redislock.Locker] does NOT satisfy this property:
// it relies on Redis replication, which is asynchronous, so a
// failover can hand a lock to two holders simultaneously.
//
// Quorum protects against node loss; it does NOT provide fencing
// tokens. A holder that stalls past TTL (GC pause, slow I/O) can still
// overlap with a second acquirer. Schema migrations and other
// correctness-critical mutations still need an application-layer fence
// or a database-level lock (see [redislock] package limitations and
// data/lock/pgadvisory). Prefer "lock durability across node failure"
// over "lock correctness" when describing this package.
//
// # When NOT to use this package
//
//   - Single-tenant deployments where Redis runs on one node. The
//     extra pool wiring buys nothing — use [redislock.NewLocker].
//   - Hot-loop critical sections where the additional N-1 round-trips
//     per Acquire dominate. Redlock is appropriate for coordination
//     primitives (leader election, schema migrations, batch jobs),
//     not for high-frequency mutexes.
//   - Deployments that can tolerate "two holders briefly during
//     failover" — most cache-invalidation, rate-limit, and budget
//     locks fit this. Pay for quorum only when you actually need it.
//
// # Algorithm summary
//
// On Acquire, the algorithm sends `SET NX PX <ttl>` to every instance
// in parallel. The lock is held iff:
//
//  1. At least floor(N/2)+1 instances accepted the SET.
//  2. Total elapsed wall-clock time was less than the configured TTL
//     (with a clock-drift margin enforced by [go-redsync/redsync/v4]).
//
// On Release, every instance is told to delete the token-fenced key.
// Instances that were unreachable on Acquire are still attempted on
// Release so that recovered nodes do not surface stale entries.
//
// # Operational guidance
//
//   - Run an ODD number of instances (typically 3 or 5). With four
//     instances, a 2-2 partition cannot recover a quorum on either
//     side, defeating the purpose.
//   - Place each instance in a separate fault domain (AZ, host, or
//     pod). Co-locating instances negates the availability win.
//   - Synchronise clocks reasonably (NTP). Severe clock drift
//     between Redis instances and the locker can falsely shorten or
//     lengthen the effective lock duration.
//   - Tune `WithTTL` to comfortably exceed the longest expected
//     critical section. The Redlock algorithm requires TTL >>
//     network RTT to be safe.
//
// # Relationship to redislock
//
// This sub-package shares the [lock.Locker] / [lock.Lock] interfaces
// and the [redsync.v4] driver with the parent [redislock] package.
// It does NOT replace [redislock.Locker]; deployments choose between
// them based on the trade-off above.
package redlock
