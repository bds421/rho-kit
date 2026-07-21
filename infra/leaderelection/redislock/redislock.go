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

// ErrCallbackDrainTimeout is returned by Run when
// [WithCallbackDrainTimeout] is configured and the OnAcquired
// callback did not return within the configured drain window after
// leadership ended. The orphan goroutine is left running — the
// orchestrator MUST treat this as a fatal signal and restart the
// process rather than retrying the elector in-place.
var ErrCallbackDrainTimeout = errors.New("leaderelection/redislock: OnAcquired callback drain timed out")

// Elector is a [leaderelection.Elector] backed by a Redis SET-NX lock.
//
// Concurrency: [Elector.Run] must be invoked from a single goroutine —
// two concurrent Runs on the same Elector would race the leader flag
// and call user callbacks out of order. [Elector.IsLeader] is safe
// for concurrent reads.
type Elector struct {
	locker        *rlock.Locker
	key           string
	retryInterval time.Duration
	renewInterval time.Duration
	drainWarnTick time.Duration
	// drainTimeout bounds how long Run waits for OnAcquired to return
	// after leadership ends. Zero (default) preserves the wait-forever
	// strict policy; a positive value enables fail-fast shutdown via
	// [ErrCallbackDrainTimeout]. See [WithCallbackDrainTimeout] for the
	// full semantics.
	drainTimeout time.Duration
	logger       *slog.Logger
	metrics      callbackDrainMetrics

	leader  atomic.Bool
	started atomic.Bool
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
		panic("leaderelection/redislock: WithMetrics requires non-nil metrics (omit the option for no metrics)")
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
		panic("leaderelection/redislock: WithCallbackDrainWarnInterval requires a positive duration")
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
// wrapped [ErrCallbackDrainTimeout]. The orphan goroutine continues
// running (Go has no goroutine kill) so the orchestrator MUST treat
// the timeout as fatal and restart the process rather than retrying
// the elector in-place.
//
// The duration must be positive.
func WithCallbackDrainTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("leaderelection/redislock: WithCallbackDrainTimeout requires a positive duration")
	}
	return func(e *Elector) { e.drainTimeout = d }
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
		panic("leaderelection/redislock: NewWithLocker locker must not be nil")
	}
	if key == "" {
		panic("leaderelection/redislock: NewWithLocker key must not be empty")
	}
	e := &Elector{
		locker:        locker,
		key:           key,
		retryInterval: 5 * time.Second,
		renewInterval: 5 * time.Second,
		drainWarnTick: 30 * time.Second,
		logger:        slog.Default(),
	}
	for _, o := range opts {
		if o == nil {
			panic("leaderelection/redislock: NewWithLocker option must not be nil")
		}
		o(e)
	}
	if e.metrics != nil {
		validateMetricKeyLabel(e.key)
	}
	return e
}

// IsLeader reports whether this replica currently believes it holds
// leadership. Eventually consistent — see [leaderelection.Elector.IsLeader]
// for the same caveat.
func (e *Elector) IsLeader() bool {
	return e.leader.Load()
}

// Run blocks while trying to acquire and hold leadership. Single-goroutine
// only — see [Elector] type docs.
func (e *Elector) Run(ctx context.Context, cb leaderelection.Callbacks) error {
	if ctx == nil {
		return errors.New("leader-election: Run requires a non-nil context")
	}
	if !e.started.CompareAndSwap(false, true) {
		return errors.New("leader-election: Run already invoked concurrently on this Elector — a second concurrent Run would race the leader flag and call OnAcquired / OnLost out of order")
	}
	// Allow re-Run after return so orchestrators can wrap Run in a
	// retry loop (mirrors k8slease / Elector interface contract).
	defer e.started.Store(false)

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
		// OnLost errors/panics are non-fatal: log and continue so a flaky
		// cleanup hook cannot permanently disable leadership (aligned
		// with etcd/k8slease and the Elector interface contract).
		if lostErr != nil {
			e.logger.Error("leader-election: OnLost failed; continuing",
				redact.String("key", e.key),
				redact.Error(lostErr),
			)
		}
		// A drain-timeout means an OnAcquired goroutine from the
		// previous term is still running inside this process. We have
		// no way to interrupt it (Go has no goroutine kill), so
		// retrying acquire would risk a within-process double-leader:
		// the new term could call OnAcquired again while the orphan
		// goroutine still holds resources. Bail out and let the
		// orchestrator restart the process (L-141).
		if holdErr != nil && errors.Is(holdErr, ErrCallbackDrainTimeout) {
			e.logger.Error("leader-election: OnAcquired drain timed out; refusing to re-acquire — restart the process",
				redact.String("key", e.key),
				redact.Error(holdErr),
			)
			return holdErr
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

// callbackResult is the value sent on cbDone when the OnAcquired
// goroutine exits — either normally (zero value) or via panic
// (panicValue captures recover()). The timedOut flag is set by
// [Elector.awaitCallbackDrain] when [WithCallbackDrainTimeout] is
// configured and the goroutine fails to signal before the deadline;
// the orphan goroutine continues running and the elector returns
// [ErrCallbackDrainTimeout].
type callbackResult struct {
	panicValue any
	timedOut   bool
}

// holdLeadership runs the OnAcquired callback and renews the lock on
// the renewInterval cadence. Returns only after the callback has
// exited, so a retry cannot overlap with leader work from the previous
// term inside this process. A callback that ignores cancellation stalls
// this elector rather than letting the same process enter leadership twice.
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

	renewTicker := time.NewTicker(e.renewInterval)
	defer renewTicker.Stop()

	awaitCallback := func() callbackResult {
		return e.awaitCallbackDrain(cbDone)
	}

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
			// Leadership is ending: drop the leader flag before the
			// (possibly long) callback drain so IsLeader stops
			// reporting true while another replica may already lead.
			// Mirrors pgadvisory.holdLeadership / k8slease.
			e.leader.Store(false)
			cancel()
			return joinDrainResult(parent.Err(), awaitCallback())
		case result := <-cbDone:
			// Happy path: the callback returned on its own while still
			// leader. Drain wait is ~0 (no cancellation was needed), so
			// do NOT record time-since-callback-start into
			// callback_drain_seconds{state=drained} — that would inflate
			// the drain SLO with whole-term length. Aligns with
			// pgadvisory/etcd, which record nothing on voluntary return.
			if result.panicValue != nil {
				return onAcquiredPanicError(result.panicValue)
			}
			return nil
		case <-renewTicker.C:
			ok, err := e.extendOnce(ctx, handle)
			if err != nil {
				// Loss detected: drop the leader flag immediately so
				// IsLeader reflects reality during the callback drain.
				e.leader.Store(false)
				cancel()
				return joinDrainResult(redact.WrapError("extend", err), awaitCallback())
			}
			if !ok {
				e.leader.Store(false)
				cancel()
				return joinDrainResult(errors.New("leader-election: handle reports lost"), awaitCallback())
			}
		}
	}
}

// extendOnce performs a single lock renewal bounded by a per-call
// deadline of one renewInterval. Without this bound a hung Extend (a
// Redis client configured without a read timeout) would block the
// renew loop indefinitely: the lock TTLs out, a competing replica
// becomes leader, yet OnAcquired's ctx stays un-cancelled because the
// loop is parked inside Extend rather than reaching cancel(). A
// deadline-exceeded Extend surfaces as a renewal error, which the
// caller treats as a lost lock — keeping the overlap window bounded to
// roughly one renewal interval as the package doc promises.
//
// The deadline derives from ctx, so leader-ctx cancellation still
// unblocks the call promptly.
func (e *Elector) extendOnce(ctx context.Context, handle lock.Lock) (bool, error) {
	extendCtx, cancel := context.WithTimeout(ctx, e.renewInterval)
	defer cancel()
	return handle.Extend(extendCtx)
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
