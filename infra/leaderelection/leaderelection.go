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
//   - infra/leaderelection/k8slease — coordination.k8s.io/v1 Lease
//     object via k8s.io/client-go for Kubernetes-native deployments.
//     Recommended when the service already runs on k8s and the
//     operator wants kubectl-visible leadership state.
//   - infra/leaderelection/etcd — etcd concurrency Session/Election
//     via go.etcd.io/etcd/client/v3. Recommended for bare-metal/VM
//     deployments that already run etcd and need strong-consistency
//     election without Kubernetes.
//
// The contract is: one leadership term calls OnAcquired at a time for
// backends that provide strong exclusion. When ctx cancels, the
// OnAcquired context is cancelled, OnAcquired must return, then OnLost
// runs synchronously before the implementation releases or retries the
// term. Backend packages document any weaker guarantees such as Redis
// TTL overlap windows.
//
// # Callback-drain metrics
//
// Each backend registers leaderelection_callback_drain_seconds and
// leaderelection_callback_drain_warn_total with the shared labels
// {backend,target,state} and {backend,target}, respectively. target is the
// backend's validated static election coordinate (for Kubernetes,
// "namespace/name"). The common descriptor shape allows multiple backends to
// share one Prometheus registerer during migrations without a registration
// panic.
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
	// to give up (unrecoverable backend error such as a callback drain
	// timeout that leaves an orphan OnAcquired goroutine).
	//
	// Reusability: after Run returns, a subsequent Run on the same
	// Elector is allowed (single-goroutine ownership still applies —
	// concurrent Run calls are rejected). lifecycle.Runner and similar
	// orchestrators may wrap Run in a retry loop.
	//
	// While the caller holds leadership, callbacks.OnAcquired runs for
	// the term. callbacks.OnLost is invoked exactly once for each
	// acquired term after callbacks.OnAcquired has returned.
	//
	// OnAcquired early return (callback returns while still leader):
	// redislock, pgadvisory, and etcd relinquish the term, run OnLost,
	// and re-enter the acquire loop. k8slease keeps renewing the Lease
	// via client-go until the lease is truly lost — OnLost is NOT
	// called on a voluntary OnAcquired return. Callers that need the
	// same semantics on every backend should keep OnAcquired blocked
	// for the whole term (select on ctx.Done()).
	//
	// OnLost errors/panics are logged and do not permanently kill the
	// elector loop; the implementation continues (or returns only for
	// ctx cancellation / unrecoverable drain timeout).
	Run(ctx context.Context, callbacks Callbacks) error

	// IsLeader is a non-blocking, eventually-consistent leadership
	// check. Use it to gate per-tick decisions inside a long-running
	// leader callback (e.g. cron jobs, batch sweepers).
	IsLeader() bool
}
