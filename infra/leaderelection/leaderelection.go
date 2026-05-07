// Package leaderelection defines the [Elector] interface used by the
// kit's leader-election primitives.
//
// Implementations live in subpackages so consumers only depend on the
// backend they use:
//
//   - infra/leaderelection/pgadvisory — Postgres advisory lock.
//     Recommended when the service already has a Postgres dependency.
//   - infra/leaderelection/k8slease (TODO) — coordination.k8s.io
//     Lease object. Recommended for k8s-native deployments.
//   - infra/leaderelection/redislock (TODO) — wraps redislock with a
//     renew loop. Use when neither Postgres nor k8s is in the path.
//
// The contract is: exactly one Run goroutine across all replicas
// observes its OnAcquired callback at any time. When ctx cancels or
// the underlying lease is lost, OnLost fires and Run returns; another
// replica becomes leader.
package leaderelection

import "context"

// Callbacks bundle leader-state transitions. Both callbacks may be nil.
//
// OnAcquired is invoked once per leadership term, in the goroutine that
// called Run. The callback's ctx is cancelled when leadership is lost
// (renewal failure, ctx cancellation by the caller). Long-running
// leader work should observe ctx and exit promptly.
//
// OnLost is invoked synchronously after OnAcquired's ctx cancels, in
// the same goroutine. It runs synchronously so the caller can
// guarantee any leader-state cleanup completes before Run returns.
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
	// While the caller holds leadership, callbacks.OnAcquired runs in
	// the same goroutine. callbacks.OnLost is invoked just before Run
	// returns, even if the caller never became leader.
	Run(ctx context.Context, callbacks Callbacks) error

	// IsLeader is a non-blocking, eventually-consistent leadership
	// check. Use it to gate per-tick decisions inside a long-running
	// leader callback (e.g. cron jobs, batch sweepers).
	IsLeader() bool
}
