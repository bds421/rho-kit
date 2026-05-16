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

// ErrCallbackDrainTimeout is returned by Run when
// [WithCallbackDrainTimeout] is configured and the OnAcquired
// callback did not return within the configured drain window after
// leadership ended. The orphan goroutine is left to finish on its
// own — the orchestrator MUST treat this as a fatal signal and
// restart the process rather than retrying the elector in-place.
var ErrCallbackDrainTimeout = errors.New("leaderelection/pgadvisory: OnAcquired callback drain timed out")

// Elector is a [leaderelection.Elector] backed by a Postgres advisory
// lock.
//
// Concurrency: [Elector.Run] must be invoked from a single goroutine —
// two concurrent Runs on the same Elector would race the leader flag
// and call user callbacks out of order. [Elector.IsLeader] is safe
// for concurrent reads.
type Elector struct {
	locker        *pgalock.Locker
	key           string
	retryInterval time.Duration
	healthCheck   time.Duration
	drainWarnTick time.Duration
	// drainTimeout bounds how long Run waits for OnAcquired to return
	// after leadership ends. Zero (default) preserves the wait-forever
	// strict policy. A positive value enables fail-fast shutdown: the
	// elector abandons the drain after timeout, returns
	// [ErrCallbackDrainTimeout], and lets the orchestrator move on.
	drainTimeout time.Duration
	logger       *slog.Logger
	metrics      callbackDrainMetrics

	leader  atomic.Bool
	started atomic.Bool
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

// WithMetrics enables Prometheus observability for the callback-drain
// watchdog. The elector validates [Elector] key against
// [promutil.ValidateStaticLabelValue] when this option is set, so a
// misconfigured key fails fast at construction rather than producing
// silent metric label injection.
//
// Passing nil panics so that "metrics enabled but unwired" never
// degrades into a silent no-op — omit the option entirely to opt out.
func WithMetrics(m *Metrics) Option {
	if m == nil {
		panic("leaderelection/pgadvisory: WithMetrics requires non-nil metrics (omit the option for no metrics)")
	}
	return func(e *Elector) { e.metrics = m }
}

// WithCallbackDrainWarnInterval overrides the cadence at which the
// elector logs a warning and records a pending-drain metric while
// waiting for [leaderelection.Callbacks.OnAcquired] to return after
// leadership ended. Default: 30 seconds.
//
// Tests use shorter intervals to exercise the warn path; production
// callers should leave the default unless their on-call rotation has
// a different escalation cadence.
func WithCallbackDrainWarnInterval(d time.Duration) Option {
	if d <= 0 {
		panic("leaderelection/pgadvisory: WithCallbackDrainWarnInterval requires a positive duration")
	}
	return func(e *Elector) { e.drainWarnTick = d }
}

// WithCallbackDrainTimeout caps how long the elector waits for
// [leaderelection.Callbacks.OnAcquired] to return after leadership
// ends. Default behaviour (no option) is wait-forever: a buggy
// callback that ignores ctx can pin shutdown until SIGKILL, which
// preserves the strict no-overlap-in-process invariant.
//
// Passing a positive duration enables fail-fast shutdown: when the
// timeout fires the elector logs a critical warning, records a
// drainStateTimeout metric observation, and returns Run with a
// wrapped [ErrCallbackDrainTimeout]. The orphan goroutine is left to
// finish on its own (Go has no goroutine kill), so callers that
// re-enter leader work from the same process can still observe an
// overlap until the orphan exits — the orchestrator MUST treat the
// timeout as a fatal signal and exit/restart rather than retrying.
//
// Use this option when an external orchestrator (Kubernetes, systemd)
// will SIGKILL the process within a bounded grace window anyway and
// the kit should record the stalled-callback evidence first instead
// of being silently terminated. Leave unset when in-process retry of
// the leader role is expected and overlap is unacceptable.
//
// The duration must be positive.
func WithCallbackDrainTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("leaderelection/pgadvisory: WithCallbackDrainTimeout requires a positive duration")
	}
	return func(e *Elector) { e.drainTimeout = d }
}

// New constructs an Elector that competes for `key` against every
// other replica using `db`.
func New(db *sql.DB, key string, opts ...Option) *Elector {
	if key == "" {
		panic("leaderelection/pgadvisory: New key must not be empty")
	}
	e := &Elector{
		locker:        pgalock.New(db),
		key:           key,
		retryInterval: 5 * time.Second,
		healthCheck:   time.Second,
		drainWarnTick: 30 * time.Second,
		logger:        slog.Default(),
	}
	for _, o := range opts {
		if o == nil {
			panic("leaderelection/pgadvisory: New option must not be nil")
		}
		o(e)
	}
	if e.metrics != nil {
		validateMetricKeyLabel(e.key)
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

// Run blocks while trying to acquire and hold leadership. Single-goroutine
// only — see [Elector] type docs. See [leaderelection.Elector.Run] for
// callback semantics.
func (e *Elector) Run(ctx context.Context, cb leaderelection.Callbacks) error {
	// Validate ctx BEFORE flipping the started flag — wave 68 closed
	// a hostile-review finding that a nil-ctx call poisoned the
	// one-shot started state and prevented any future Run from
	// running.
	if ctx == nil {
		return errors.New("leader-election: Run requires a non-nil context")
	}
	if !e.started.CompareAndSwap(false, true) {
		return errors.New("leader-election: Run already invoked on this Elector — a second Run would race the leader flag and call OnLeader / OnRelinquish out of order")
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

// callbackResult is the value sent on cbDone when the OnAcquired
// goroutine exits — either normally (zero value) or via panic
// (panicValue captures recover()). The timedOut flag is set by
// [Elector.awaitCallbackDrain] when [WithCallbackDrainTimeout] is
// configured and the goroutine fails to signal before the deadline;
// the orphan goroutine continues running in that case (Go has no
// goroutine kill), so the elector returns [ErrCallbackDrainTimeout]
// and lets the orchestrator terminate the process.
type callbackResult struct {
	panicValue any
	timedOut   bool
}

// holdLeadership runs the OnAcquired callback while a sub-goroutine
// pings the connection to detect loss. Returns only after the callback
// has exited, so a retry cannot overlap with leader work from the
// previous term inside this process. A callback that ignores cancellation
// stalls this elector rather than letting the same process enter leadership
// twice.
//
// The drain watchdog logs a warning and records a pending-drain
// observation every [Elector.drainWarnTick] (default 30s) while
// waiting on a stalled OnAcquired — round-3 removed the hard drain
// timeout, so this is the only operator-visible signal that a buggy
// callback is pinning the elector.
func (e *Elector) holdLeadership(parent context.Context, handle lock.Lock, cb leaderelection.Callbacks) error {
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

	awaitCallback := func() callbackResult {
		return e.awaitCallbackDrain(cbDone)
	}

	// joinDrainResult merges the drain-timeout signal (if any) into the
	// termination error. Panic errors win over plain timeout for clarity
	// — a panicking callback that also blew its drain budget should
	// surface both via errors.Is for the dashboards' sake.
	joinDrainResult := func(termErr error, result callbackResult) error {
		errs := []error{}
		if termErr != nil {
			errs = append(errs, termErr)
		}
		if result.panicValue != nil {
			errs = append(errs, onAcquiredPanicError(result.panicValue))
		}
		if result.timedOut {
			errs = append(errs, ErrCallbackDrainTimeout)
		}
		switch len(errs) {
		case 0:
			return nil
		case 1:
			return errs[0]
		default:
			return errors.Join(errs...)
		}
	}

	for {
		select {
		case <-parent.Done():
			cancel()
			return joinDrainResult(parent.Err(), awaitCallback())
		case result := <-cbDone:
			if result.panicValue != nil {
				return onAcquiredPanicError(result.panicValue)
			}
			return nil
		case <-healthTicker.C:
			if ok, err := handle.Extend(ctx); err != nil {
				cancel()
				return joinDrainResult(fmt.Errorf("extend: %w", err), awaitCallback())
			} else if !ok {
				cancel()
				return joinDrainResult(errors.New("leader-election: handle reports lost"), awaitCallback())
			}
		}
	}
}

// awaitCallbackDrain blocks until the OnAcquired goroutine has
// signalled completion via cbDone. While waiting it emits a warn log
// and (if metrics are configured) records a pending-drain observation
// every drainWarnTick so a stalled callback is operator-visible. The
// terminal duration is always recorded — state="drained" on a normal
// return, state="timeout" when [Elector.drainTimeout] is configured
// and fires before the goroutine returns.
//
// On timeout the orphan goroutine is left running (Go has no
// goroutine kill); the elector signals the caller by returning a
// callbackResult with timedOut=true. holdLeadership lifts this into
// [ErrCallbackDrainTimeout] for the Run boundary.
func (e *Elector) awaitCallbackDrain(cbDone <-chan callbackResult) callbackResult {
	start := time.Now()
	tick := e.drainWarnTick
	if tick <= 0 {
		tick = 30 * time.Second
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	var deadline <-chan time.Time
	if e.drainTimeout > 0 {
		t := time.NewTimer(e.drainTimeout)
		defer t.Stop()
		deadline = t.C
	}

	for {
		select {
		case result := <-cbDone:
			if e.metrics != nil {
				e.metrics.observeDrainDuration(time.Since(start), e.key, drainStateDrained)
			}
			return result
		case <-deadline:
			elapsed := time.Since(start)
			logger := e.logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.Error("leader-election: OnAcquired callback drain timeout — orphan goroutine left running, orchestrator must restart process",
				redact.String("key", e.key),
				slog.Duration("elapsed", elapsed),
				slog.Duration("timeout", e.drainTimeout),
			)
			if e.metrics != nil {
				e.metrics.observeDrainDuration(elapsed, e.key, drainStateTimeout)
			}
			return callbackResult{timedOut: true}
		case <-ticker.C:
			elapsed := time.Since(start)
			logger := e.logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.Warn("leader-election: OnAcquired callback still draining",
				redact.String("key", e.key),
				slog.Duration("elapsed", elapsed),
			)
			if e.metrics != nil {
				e.metrics.observeDrainDuration(elapsed, e.key, drainStatePending)
				e.metrics.observeDrainWarn(e.key)
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
