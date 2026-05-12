// Package leaderelection defines the [Elector] interface used by the
// kit's leader-election primitives.
//
// Implementations live in subpackages so consumers only depend on the
// backend they use:
//
//   - infra/leaderelection/pgadvisory — Postgres advisory lock.
//     Recommended when the service already has a Postgres dependency.
//   - infra/leaderelection/redislock — wraps redislock with a renew
//     loop. Use when Postgres is not in the path.
//   - infra/leaderelection/k8slease (planned) — coordination.k8s.io
//     Lease object for k8s-native deployments. Track via the v2 backlog.
//
// The contract is: one leadership term calls OnAcquired at a time for
// backends that provide strong exclusion. When ctx cancels, the
// OnAcquired context is cancelled, OnAcquired must return, then OnLost
// runs synchronously before the implementation releases or retries the
// term. Backend packages document any weaker guarantees such as Redis
// TTL overlap windows.
package leaderelection

import "context"

// Callbacks bundle leader-state transitions. Both callbacks may be nil.
//
// OnAcquired is invoked once per leadership term. The callback's ctx is
// cancelled when leadership is lost (renewal failure, ctx cancellation
// by the caller). Long-running leader work must observe ctx and exit
// promptly; implementations wait for it before starting another term.
//
// OnLost is invoked synchronously once for each acquired leadership
// term, after OnAcquired returns and before the implementation starts
// another term or returns from Run. It is not invoked when leadership
// was never acquired.
type Callbacks struct {
	OnAcquired func(ctx context.Context)
	OnLost     func()
}

// Elector continuously tries to acquire and hold leadership.
type Elector interface {
	// Run blocks while attempting to acquire leadership. Returns when
	// ctx cancels (caller-initiated shutdown) or the elector decides
	// to give up (unrecoverable backend error).
	//
	// While the caller holds leadership, callbacks.OnAcquired runs for
	// the term. callbacks.OnLost is invoked exactly once for each
	// acquired term after callbacks.OnAcquired has returned.
	Run(ctx context.Context, callbacks Callbacks) error

	// IsLeader is a non-blocking, eventually-consistent leadership
	// check. Use it to gate per-tick decisions inside a long-running
	// leader callback (e.g. cron jobs, batch sweepers).
	IsLeader() bool
}
