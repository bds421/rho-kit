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

	"github.com/bds421/rho-kit/data/lock"
	rlock "github.com/bds421/rho-kit/data/lock/redislock"
	"github.com/bds421/rho-kit/infra/leaderelection"
)

// Elector is a [leaderelection.Elector] backed by a Redis SET-NX lock.
type Elector struct {
	locker        *rlock.Locker
	key           string
	retryInterval time.Duration
	renewInterval time.Duration
	logger        *slog.Logger

	leader atomic.Bool
}

// Option configures the Elector.
type Option func(*Elector)

// WithRetryInterval controls how often a non-leader replica retries the
// acquire. Default: 5 seconds.
func WithRetryInterval(d time.Duration) Option {
	return func(e *Elector) { e.retryInterval = d }
}

// WithRenewInterval sets how often the leader extends the lock TTL.
// Must be substantially shorter than the lock TTL configured on the
// underlying [rlock.Locker]; otherwise the lock can expire between
// renewals during normal operation. Default: 5 seconds (suitable for
// the redislock default TTL of 30s).
func WithRenewInterval(d time.Duration) Option {
	return func(e *Elector) { e.renewInterval = d }
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
		locker:        locker,
		key:           key,
		retryInterval: 5 * time.Second,
		renewInterval: 5 * time.Second,
		logger:        slog.Default(),
	}
	for _, o := range opts {
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
	defer func() {
		if cb.OnLost != nil {
			cb.OnLost()
		}
	}()

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		handle, ok, err := e.locker.Acquire(ctx, e.key)
		if err != nil {
			e.logger.Warn("leader-election: acquire failed",
				slog.String("key", e.key),
				slog.Any("error", err),
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
		e.logger.Info("leader-election: acquired", slog.String("key", e.key))

		leaderCtx, leaderCancel := context.WithCancel(ctx)
		holdErr := e.holdLeadership(leaderCtx, handle, cb)
		leaderCancel()
		e.leader.Store(false)
		_ = handle.Release(context.Background())

		if errors.Is(holdErr, context.Canceled) {
			return ctx.Err()
		}
		e.logger.Warn("leader-election: leadership lost; retrying",
			slog.String("key", e.key),
			slog.Any("error", holdErr),
		)
	}
}

// holdLeadership runs the OnAcquired callback in this goroutine and
// renews the lock on the renewInterval cadence. Returns when the
// callback finishes, ctx cancels, or renewal fails.
func (e *Elector) holdLeadership(ctx context.Context, handle lock.Lock, cb leaderelection.Callbacks) error {
	cbDone := make(chan struct{})
	go func() {
		defer close(cbDone)
		if cb.OnAcquired != nil {
			cb.OnAcquired(ctx)
		}
	}()

	renewTicker := time.NewTicker(e.renewInterval)
	defer renewTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			<-cbDone
			return ctx.Err()
		case <-cbDone:
			return nil
		case <-renewTicker.C:
			ok, err := handle.Extend(ctx)
			if err != nil {
				return fmt.Errorf("extend: %w", err)
			}
			if !ok {
				return errors.New("leader-election: handle reports lost")
			}
		}
	}
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
