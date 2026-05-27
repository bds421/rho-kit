// Package redislock provides distributed mutual exclusion using Redis.
//
// Wave 126 migrated this package to wrap
// [github.com/go-redsync/redsync/v4] in single-pool mode. The kit keeps
// its [Locker] / [lock.Lock] surface; redsync owns the Lua scripts,
// retry loop, and token-fenced release/extend semantics. Redsync's
// Redlock multi-master quorum mode is NOT used — single-pool keeps the
// kit on the same operational contract as before, with [DegradedLocker]
// as the Redis-outage fallback.
//
// # Limitations
//
// This is an advisory lock backed by a single Redis instance using
// SET NX with a TTL. It is suitable for leader election, deduplication,
// and coordination where occasional double-execution is acceptable. It
// is NOT suitable for correctness-critical mutual exclusion because:
//
//   - No fencing tokens: a slow holder that outlives the TTL cannot
//     detect that another process has since acquired the lock. If the
//     lock holder's TTL expires while it is still processing (e.g.
//     due to a GC pause or slow I/O), a second process can acquire
//     the lock and both may write to shared resources concurrently.
//   - Single-node: if Redis restarts, all locks are lost. Redlock
//     quorum is intentionally not adopted: the consensus argument is
//     contested and the kit prefers a single, well-understood failure
//     mode over a quorum that masks correctness gaps.
//   - Clock-dependent: TTL accuracy depends on Redis and client clocks
//     agreeing within reasonable bounds.
//
// For critical sections that write to databases, use database-level
// locking (SELECT ... FOR UPDATE) or implement fencing tokens at the
// application layer. For Postgres-backed work, see
// [github.com/bds421/rho-kit/data/lock/pgadvisory/v2] for session-
// scoped advisory locks that automatically release on connection
// death.
//
// # Usage
//
// Prefer the [Locker] API:
//
//	lc := redislock.NewLocker(client, redislock.WithTTL(30*time.Second))
//	if err := lc.WithLock(ctx, "order:42", func(ctx context.Context) error {
//	    // critical section
//	    return nil
//	}); err != nil {
//	    if errors.Is(err, lock.ErrLockLost) {
//	        // TTL expired mid-section — caller must reconcile
//	    }
//	    return err
//	}
//
// See also: [github.com/bds421/rho-kit/data/v2/lock] for the
// backend-neutral interface and [github.com/bds421/rho-kit/data/lock/pgadvisory/v2]
// for the session-scoped Postgres alternative.
package redislock
