// Package redislock implements [leaderelection.Elector] on top of
// data/lock/redislock.
//
// One leader per (Redis instance, key) tuple across all replicas: the
// elector holds a Redis SET-NX lock and renews it on a fixed cadence.
// Renewal failure (network blip, Redis failover, key eviction) cancels
// the leader ctx so OnAcquired can drain and a competing replica can
// step in.
//
// Recommended when:
//
//   - The service has Redis but neither Postgres nor a kubernetes
//     control plane.
//   - The leader work tolerates the brief overlap window inherent to
//     TTL-based locks (see fencing-token caveat below).
//
// CRITICAL fencing caveat: Redis-based locks lack fencing tokens. If
// the leader stalls (GC pause, slow disk) past the TTL, a second
// replica can become leader before the first notices it lost the lock.
// Both will believe themselves leader for one renewal interval. For
// work that must NEVER overlap (e.g. exclusive writes to a shared
// resource), use [pgadvisory] or implement application-level fencing
// on top of this elector.
package redislock

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/bds421/rho-kit/core/v2/redact"
	rlock "github.com/bds421/rho-kit/data/lock/redislock/v2"
	"github.com/bds421/rho-kit/data/v2/lock"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
)

// defaultCallbackDrainTimeout bounds how long holdLeadership waits for
// a buggy OnAcquired callback that ignores ctx after a renewal failure
// before returning and leaving the callback goroutine detached.
const defaultCallbackDrainTimeout = 30 * time.Second

// Elector is a [leaderelection.Elector] backed by a Redis SET-NX lock.
type Elector struct {
	locker               *rlock.Locker
	key                  string
	retryInterval        time.Duration
	renewInterval        time.Duration
	callbackDrainTimeout time.Duration
	logger               *slog.Logger

	leader atomic.Bool
}

// Option configures the Elector.
type Option func(*Elector)

// WithRetryInterval controls how often a non-leader replica retries the
// acquire. Default: 5 seconds.
func WithRetryInterval(d time.Duration) Option {
	if d <= 0 {
		panic("leaderelection/redislock: WithRetryInterval requires a positive duration")
	}
	return func(e *Elector) { e.retryInterval = d }
}

// WithRenewInterval sets how often the leader extends the lock TTL.
// Must be substantially shorter than the lock TTL configured on the
// underlying [rlock.Locker]; otherwise the lock can expire between
// renewals during normal operation. Default: 5 seconds (suitable for
// the redislock default TTL of 30s).
func WithRenewInterval(d time.Duration) Option {
	if d <= 0 {
		panic("leaderelection/redislock: WithRenewInterval requires a positive duration")
	}
	return func(e *Elector) { e.renewInterval = d }
}

// WithCallbackDrainTimeout bounds how long Elector waits for the
// OnAcquired callback to honour ctx after a renewal failure or parent
// cancellation. Once the timeout elapses the elector returns and the
// goroutine runs detached. Default: 30 seconds.
func WithCallbackDrainTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("leaderelection/redislock: WithCallbackDrainTimeout requires a positive duration")
	}
	return func(e *Elector) { e.callbackDrainTimeout = d }
}

// WithLogger sets the logger. A nil logger is normalized to [slog.Default]
// so the elector never holds a nil slog.Logger.
func WithLogger(l *slog.Logger) Option {
	return func(e *Elector) {
		if l == nil {
			e.logger = slog.Default()
			return
		}
		e.logger = l
	}
}

// New constructs an Elector that competes for `key` against every other
// replica using `client`.
//
// The elector uses a [rlock.Locker] with default TTL (30s). Callers
// that need a non-default TTL should construct the locker themselves
// and pass it via [NewWithLocker].
func New(client redis.UniversalClient, key string, opts ...Option) *Elector {
	return NewWithLocker(rlock.NewLocker(client), key, opts...)
}

// NewWithLocker is the explicit form for callers who want to control
// the underlying locker's TTL or retry behaviour.
func NewWithLocker(locker *rlock.Locker, key string, opts ...Option) *Elector {
	if locker == nil {
		panic("leaderelection/redislock: locker must not be nil")
	}
	if key == "" {
		panic("leaderelection/redislock: key must not be empty")
	}
	e := &Elector{
		locker:               locker,
		key:                  key,
		retryInterval:        5 * time.Second,
		renewInterval:        5 * time.Second,
		callbackDrainTimeout: defaultCallbackDrainTimeout,
		logger:               slog.Default(),
	}
	for _, o := range opts {
		if o == nil {
			panic("leaderelection/redislock: option must not be nil")
		}
		o(e)
	}
	return e
}

// IsLeader reports whether this replica currently believes it holds
// leadership. Eventually consistent — see [leaderelection.Elector.IsLeader]
// for the same caveat.
func (e *Elector) IsLeader() bool {
	return e.leader.Load()
}

// Run blocks while trying to acquire and hold leadership.
func (e *Elector) Run(ctx context.Context, cb leaderelection.Callbacks) error {
	if ctx == nil {
		return errors.New("leader-election: Run requires a non-nil context")
	}

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		handle, ok, err := e.locker.Acquire(ctx, e.key)
		if err != nil {
			e.logger.Warn("leader-election: acquire failed",
				redact.String("key", e.key),
				redact.Error(err),
			)
			if !sleep(ctx, e.retryInterval) {
				return ctx.Err()
			}
			continue
		}
		if !ok {
			if !sleep(ctx, e.retryInterval) {
				return ctx.Err()
			}
			continue
		}

		e.leader.Store(true)
		e.logger.Info("leader-election: acquired", redact.String("key", e.key))

		holdErr := e.holdLeadership(ctx, handle, cb)
		e.leader.Store(false)
		lostErr := e.runOnLost(cb)
		// Bound Release: a hung Redis must not pin this goroutine
		// indefinitely and starve the elector loop.
		releaseCtx, releaseCancel := leaderReleaseContext(ctx, 5*time.Second)
		if err := handle.Release(releaseCtx); err != nil && !errors.Is(err, lock.ErrLockLost) {
			e.logger.Warn("leader-election: release failed; lock will TTL out",
				redact.String("key", e.key),
				redact.Error(err),
			)
		}
		releaseCancel()

		if ctx.Err() != nil {
			return errors.Join(ctx.Err(), holdErr, lostErr)
		}
		if lostErr != nil {
			return errors.Join(holdErr, lostErr)
		}
		if holdErr == nil {
			e.logger.Info("leader-election: leadership callback returned; retrying",
				redact.String("key", e.key),
			)
			if !sleep(ctx, e.retryInterval) {
				return ctx.Err()
			}
			continue
		}
		e.logger.Warn("leader-election: leadership lost; retrying",
			redact.String("key", e.key),
			redact.Error(holdErr),
		)
	}
}

func (e *Elector) runOnLost(cb leaderelection.Callbacks) (err error) {
	if cb.OnLost == nil {
		return nil
	}
	defer func() {
		if rec := recover(); rec != nil {
			logger := e.logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.Error("leader-election: OnLost callback panicked",
				redact.String("key", e.key),
				redact.Panic(rec),
			)
			err = fmt.Errorf("leader-election: OnLost panic: %s", redact.PanicValue(rec))
		}
	}()
	cb.OnLost()
	return nil
}

func leaderReleaseContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// holdLeadership runs the OnAcquired callback and renews the lock on
// the renewInterval cadence. Returns only after the callback has
// exited, so a retry cannot overlap with leader work from the previous
// term inside this process.
func (e *Elector) holdLeadership(parent context.Context, handle lock.Lock, cb leaderelection.Callbacks) error {
	type callbackResult struct {
		panicValue any
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	cbDone := make(chan callbackResult, 1)
	go func() {
		var result callbackResult
		defer func() {
			if rec := recover(); rec != nil {
				result.panicValue = rec
			}
			cbDone <- result
		}()
		if cb.OnAcquired != nil {
			cb.OnAcquired(ctx)
		}
	}()

	renewTicker := time.NewTicker(e.renewInterval)
	defer renewTicker.Stop()

	awaitCallback := func() (callbackResult, bool) {
		// A zero timeout disables the bound (block forever). Production
		// constructors set a sane default; this branch covers tests that
		// build Elector literals directly.
		if e.callbackDrainTimeout <= 0 {
			res := <-cbDone
			return res, true
		}
		t := time.NewTimer(e.callbackDrainTimeout)
		defer t.Stop()
		select {
		case res := <-cbDone:
			return res, true
		case <-t.C:
			logger := e.logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.Error("leader-election: OnAcquired ignored cancellation; returning while callback runs detached",
				redact.String("key", e.key),
				"timeout", e.callbackDrainTimeout,
			)
			return callbackResult{}, false
		}
	}

	for {
		select {
		case <-parent.Done():
			cancel()
			result, drained := awaitCallback()
			if !drained {
				return parent.Err()
			}
			if result.panicValue != nil {
				return errors.Join(parent.Err(), onAcquiredPanicError(result.panicValue))
			}
			return parent.Err()
		case result := <-cbDone:
			if result.panicValue != nil {
				return onAcquiredPanicError(result.panicValue)
			}
			return nil
		case <-renewTicker.C:
			ok, err := handle.Extend(ctx)
			if err != nil {
				termErr := fmt.Errorf("extend: %w", err)
				cancel()
				result, drained := awaitCallback()
				if !drained {
					return termErr
				}
				if result.panicValue != nil {
					return errors.Join(termErr, onAcquiredPanicError(result.panicValue))
				}
				return termErr
			}
			if !ok {
				termErr := errors.New("leader-election: handle reports lost")
				cancel()
				result, drained := awaitCallback()
				if !drained {
					return termErr
				}
				if result.panicValue != nil {
					return errors.Join(termErr, onAcquiredPanicError(result.panicValue))
				}
				return termErr
			}
		}
	}
}

func onAcquiredPanicError(rec any) error {
	return fmt.Errorf("leader-election: OnAcquired panic: %s", redact.PanicValue(rec))
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
