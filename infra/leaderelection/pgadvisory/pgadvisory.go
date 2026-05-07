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

	"github.com/bds421/rho-kit/data/lock"
	pgalock "github.com/bds421/rho-kit/data/lock/pgadvisory"
	"github.com/bds421/rho-kit/infra/leaderelection"
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
	return func(e *Elector) { e.retryInterval = d }
}

// WithHealthCheck controls how often the leader pings its connection
// to detect lost-leader scenarios (network blip, server failover).
// Default: 1 second.
func WithHealthCheck(d time.Duration) Option {
	return func(e *Elector) { e.healthCheck = d }
}

// WithLogger sets the logger. Default: slog.Default.
func WithLogger(l *slog.Logger) Option {
	return func(e *Elector) { e.logger = l }
}

// New constructs an Elector that competes for `key` against every
// other replica using `db`.
func New(db *sql.DB, key string, opts ...Option) *Elector {
	e := &Elector{
		locker:        pgalock.New(db),
		key:           key,
		retryInterval: 5 * time.Second,
		healthCheck:   time.Second,
		logger:        slog.Default(),
	}
	for _, o := range opts {
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
			// Another replica holds it. Back off and retry.
			if !sleep(ctx, e.retryInterval) {
				return ctx.Err()
			}
			continue
		}

		// We are leader.
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
		// Lost leadership for non-cancellation reasons (renewal
		// failure). Loop to retry.
		e.logger.Warn("leader-election: leadership lost; retrying",
			slog.String("key", e.key),
			slog.Any("error", holdErr),
		)
	}
}

// holdLeadership runs the OnAcquired callback in this goroutine while
// a sub-goroutine pings the connection to detect loss. Returns when
// the callback finishes, ctx cancels, or the connection is lost.
func (e *Elector) holdLeadership(ctx context.Context, handle lock.Lock, cb leaderelection.Callbacks) error {
	cbDone := make(chan struct{})
	go func() {
		defer close(cbDone)
		if cb.OnAcquired != nil {
			cb.OnAcquired(ctx)
		}
	}()

	healthTicker := time.NewTicker(e.healthCheck)
	defer healthTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			<-cbDone // wait for caller to release leader work
			return ctx.Err()
		case <-cbDone:
			return nil
		case <-healthTicker.C:
			if ok, err := handle.Extend(ctx); err != nil {
				return fmt.Errorf("extend: %w", err)
			} else if !ok {
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
