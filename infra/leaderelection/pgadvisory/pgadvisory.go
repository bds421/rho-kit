// Package pgadvisory implements [leaderelection.Elector] on top of
// data/lock/pgadvisory.
//
// One leader per (db, key) tuple across all replicas: the elector
// holds a session-scoped Postgres advisory lock and considers itself
// the leader as long as that connection is alive. Renewal is
// automatic — the lock is held for the connection's lifetime, so we
// only need to detect connection loss to trigger a re-election.
//
// Recommended when:
//   - The service has a Postgres dependency.
//   - The leader work is light enough to tolerate the connection-pin
//     cost (one connection out of the pool while leader).
//
// Cost: one connection from the pool while leader. Size your pool's
// MaxOpenConns accordingly.
package pgadvisory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
	pgalock "github.com/bds421/rho-kit/data/lock/pgadvisory/v2"
	"github.com/bds421/rho-kit/data/v2/lock"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
)

// Elector is a [leaderelection.Elector] backed by a Postgres advisory
// lock.
type Elector struct {
	locker        *pgalock.Locker
	key           string
	retryInterval time.Duration
	healthCheck   time.Duration
	logger        *slog.Logger

	leader atomic.Bool
}

// Option configures the Elector.
type Option func(*Elector)

// WithRetryInterval controls how often a non-leader replica retries
// the acquire. Default: 5 seconds.
func WithRetryInterval(d time.Duration) Option {
	if d <= 0 {
		panic("leaderelection/pgadvisory: WithRetryInterval requires a positive duration")
	}
	return func(e *Elector) { e.retryInterval = d }
}

// WithHealthCheck controls how often the leader pings its connection
// to detect lost-leader scenarios (network blip, server failover).
// Default: 1 second.
func WithHealthCheck(d time.Duration) Option {
	if d <= 0 {
		panic("leaderelection/pgadvisory: WithHealthCheck requires a positive duration")
	}
	return func(e *Elector) { e.healthCheck = d }
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

// New constructs an Elector that competes for `key` against every
// other replica using `db`.
func New(db *sql.DB, key string, opts ...Option) *Elector {
	if key == "" {
		panic("leaderelection/pgadvisory: key must not be empty")
	}
	e := &Elector{
		locker:        pgalock.New(db),
		key:           key,
		retryInterval: 5 * time.Second,
		healthCheck:   time.Second,
		logger:        slog.Default(),
	}
	for _, o := range opts {
		if o == nil {
			panic("leaderelection/pgadvisory: option must not be nil")
		}
		o(e)
	}
	return e
}

// IsLeader reports whether this replica currently believes it holds
// leadership. The result is eventually consistent — between the loss
// signal and the atomic flip, IsLeader may briefly return true after
// a real leadership loss. Production decisions that must not run on a
// non-leader should be inside the OnAcquired callback's goroutine
// where ctx cancellation is the canonical "lost" signal.
func (e *Elector) IsLeader() bool {
	return e.leader.Load()
}

// Run blocks while trying to acquire and hold leadership. See
// [leaderelection.Elector.Run] for callback semantics.
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
			// Another replica holds it. Back off and retry.
			if !sleep(ctx, e.retryInterval) {
				return ctx.Err()
			}
			continue
		}

		// We are leader.
		e.leader.Store(true)
		e.logger.Info("leader-election: acquired", redact.String("key", e.key))

		holdErr := e.holdLeadership(ctx, handle, cb)
		e.leader.Store(false)
		lostErr := e.runOnLost(cb)
		releaseCtx, releaseCancel := leaderReleaseContext(ctx, 5*time.Second)
		if err := handle.Release(releaseCtx); err != nil && !errors.Is(err, lock.ErrLockLost) {
			e.logger.Warn("leader-election: release failed; advisory lock will release on session end",
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
		// Lost leadership for non-cancellation reasons (renewal
		// failure). Loop to retry.
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

// holdLeadership runs the OnAcquired callback while a sub-goroutine
// pings the connection to detect loss. Returns only after the callback
// has exited, so a retry cannot overlap with leader work from the
// previous term inside this process.
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

	healthTicker := time.NewTicker(e.healthCheck)
	defer healthTicker.Stop()

	for {
		select {
		case <-parent.Done():
			cancel()
			result := <-cbDone // wait for caller to release leader work
			if result.panicValue != nil {
				return errors.Join(parent.Err(), onAcquiredPanicError(result.panicValue))
			}
			return parent.Err()
		case result := <-cbDone:
			if result.panicValue != nil {
				return onAcquiredPanicError(result.panicValue)
			}
			return nil
		case <-healthTicker.C:
			if ok, err := handle.Extend(ctx); err != nil {
				termErr := fmt.Errorf("extend: %w", err)
				cancel()
				result := <-cbDone
				if result.panicValue != nil {
					return errors.Join(termErr, onAcquiredPanicError(result.panicValue))
				}
				return termErr
			} else if !ok {
				termErr := errors.New("leader-election: handle reports lost")
				cancel()
				result := <-cbDone
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
