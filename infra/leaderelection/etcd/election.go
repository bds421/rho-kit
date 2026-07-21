package etcd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
)

// Default knobs. The lease TTL and reacquire backoff are deliberately
// modest so a single transient blip recovers within a few seconds;
// operators with longer-running leader workloads can lengthen these
// to reduce churn.
const (
	defaultLeaseTTLSeconds  = 15
	defaultReacquireBackoff = 2 * time.Second
	defaultDrainWarnTick    = 30 * time.Second
	maxElectionKeyLen       = 256
)

// ErrCallbackDrainTimeout is returned by Run when
// [WithCallbackDrainTimeout] is configured and the OnAcquired
// callback did not return within the configured drain window after
// leadership ended. The orphan goroutine is left running — the
// orchestrator MUST treat this as a fatal signal and restart the
// process rather than retrying the elector in-place. Mirrors the
// shape used by [k8slease] and [pgadvisory] for cross-adapter
// consistency.
//
// On this timeout path the kit's usual "OnLost runs only after
// OnAcquired returns" no-overlap guarantee is suspended: OnLost is
// still invoked (matching [k8slease] / [pgadvisory] / redislock) but
// the orphaned OnAcquired is by definition still running, so OnLost
// may execute concurrently with it. Cleanup hooks on this path must
// not assume exclusive access to state the leader callback touches.
// This window only exists when WithCallbackDrainTimeout is set; the
// default wait-forever behaviour preserves strict no-overlap.
var ErrCallbackDrainTimeout = errors.New("leaderelection/etcd: OnAcquired callback drain timed out")

// Elector is a [leaderelection.Elector] backed by an etcd lease and
// election prefix.
//
// Concurrency: [Elector.Run] must be invoked from a single goroutine —
// a second concurrent Run would race the leader flag and call user
// callbacks out of order. [Elector.IsLeader] is safe for concurrent
// reads.
type Elector struct {
	client           *clientv3.Client
	electionKey      string
	identity         string
	leaseTTLSeconds  int
	reacquireBackoff time.Duration
	drainWarnTick    time.Duration
	drainTimeout     time.Duration
	logger           *slog.Logger
	metrics          callbackDrainMetrics

	// sessionFactory is the seam used by unit tests. Production
	// construction sets it to [defaultSessionFactory]; tests inject
	// fakes via the unexported helper in election_test.go.
	sessionFactory sessionFactory

	leader  atomic.Bool
	started atomic.Bool
}

// Option configures the Elector.
type Option func(*Elector)

// WithLeaseTTLSeconds sets the etcd lease TTL. Default: 15 seconds
// (matches the [k8slease] lease duration for cross-adapter familiarity).
//
// The TTL bounds the worst-case window in which two replicas can
// both believe they are leader after a partition: when the previous
// leader is unreachable, peers must wait for the TTL to expire
// before they can acquire. Choose a value long enough for typical
// renewal latencies (network RTT plus etcd commit time) but short
// enough to fail over within your SLO.
//
// Non-positive values panic so misconfiguration surfaces at startup.
func WithLeaseTTLSeconds(seconds int) Option {
	if seconds <= 0 {
		panic("leaderelection/etcd: WithLeaseTTLSeconds requires a positive value")
	}
	return func(e *Elector) { e.leaseTTLSeconds = seconds }
}

// WithReacquireBackoff sets the wall-clock pause between losing a
// term (or hitting a transient backend error) and the next acquire
// attempt. Default: 2 seconds.
//
// A small value minimises leadership outage time at the cost of more
// etcd round-trips when peers contend or the cluster is unhealthy.
// Set to a value comfortably under the lease TTL.
func WithReacquireBackoff(d time.Duration) Option {
	if d <= 0 {
		panic("leaderelection/etcd: WithReacquireBackoff requires a positive duration")
	}
	return func(e *Elector) { e.reacquireBackoff = d }
}

// WithLogger sets the logger. A nil logger is normalised to
// [slog.Default] so the elector never holds a nil *slog.Logger.
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
// watchdog. When this option is set, [New] validates the election
// key value against [promutil.ValidateStaticLabelValue] so a
// misconfigured caller fails fast at construction rather than
// producing silent metric label injection.
//
// Passing nil panics so that "metrics enabled but unwired" never
// degrades into a silent no-op — omit the option entirely to opt out.
func WithMetrics(m *Metrics) Option {
	if m == nil {
		panic("leaderelection/etcd: WithMetrics requires non-nil metrics (omit the option for no metrics)")
	}
	return func(e *Elector) { e.metrics = m }
}

// WithCallbackDrainWarnInterval overrides the cadence at which the
// elector logs a warning and records a pending-drain metric while
// waiting for [leaderelection.Callbacks.OnAcquired] to return after
// leadership ended. Default: 30 seconds (matches k8slease / pgadvisory
// / redislock).
func WithCallbackDrainWarnInterval(d time.Duration) Option {
	if d <= 0 {
		panic("leaderelection/etcd: WithCallbackDrainWarnInterval requires a positive duration")
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
// On the timeout path OnLost is still invoked (for cross-adapter
// symmetry with [k8slease] / [pgadvisory] / redislock) even though the
// orphaned OnAcquired has not returned, so OnLost may run concurrently
// with the stalled leader callback — see [ErrCallbackDrainTimeout].
// Cleanup hooks used with this option must tolerate that overlap; the
// default wait-forever behaviour does not have it.
//
// Use this option when an external orchestrator (Kubernetes pod
// restart, systemd) will SIGKILL the process within a bounded grace
// window anyway and the kit should record the stalled-callback
// evidence first instead of being silently terminated.
func WithCallbackDrainTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("leaderelection/etcd: WithCallbackDrainTimeout requires a positive duration")
	}
	return func(e *Elector) { e.drainTimeout = d }
}

// New constructs an Elector that competes for leadership on the etcd
// `electionKey` prefix on behalf of `identity`. Every replica wiring
// this elector MUST pass a distinct `identity` (typically the pod
// name) because etcd records it as the leader value; two replicas
// sharing an identity cannot distinguish themselves and would race
// the local leader flag.
//
// Panics on invalid argument shapes (nil client, empty election key,
// election key longer than 256 bytes, election key containing
// control bytes, empty identity, nil option) so misconfiguration
// fails fast at startup.
func New(client *clientv3.Client, electionKey, identity string, opts ...Option) *Elector {
	if client == nil {
		panic("leaderelection/etcd: New client must not be nil")
	}
	if err := validateElectionKey(electionKey); err != nil {
		panic("leaderelection/etcd: " + err.Error())
	}
	if identity == "" {
		panic("leaderelection/etcd: New identity must not be empty — use the pod name (POD_NAME env) or another per-replica unique value")
	}
	e := &Elector{
		client:           client,
		electionKey:      electionKey,
		identity:         identity,
		leaseTTLSeconds:  defaultLeaseTTLSeconds,
		reacquireBackoff: defaultReacquireBackoff,
		drainWarnTick:    defaultDrainWarnTick,
		logger:           slog.Default(),
		sessionFactory:   defaultSessionFactory(client),
	}
	for _, o := range opts {
		if o == nil {
			panic("leaderelection/etcd: New option must not be nil")
		}
		o(e)
	}
	if e.metrics != nil {
		validateMetricLabel("election", e.electionKey)
	}
	return e
}

// validateElectionKey enforces a key shape that survives both etcd's
// own constraints and the kit's metric-label validation when
// [WithMetrics] is used. Rejects empty keys, control bytes, and keys
// longer than 256 bytes.
func validateElectionKey(key string) error {
	if key == "" {
		return errors.New("New electionKey must not be empty")
	}
	if len(key) > maxElectionKeyLen {
		return fmt.Errorf("New electionKey exceeds maximum length (%d bytes)", maxElectionKeyLen)
	}
	for i := 0; i < len(key); i++ {
		c := key[i]
		if c < 0x20 || c == 0x7f {
			return errors.New("New electionKey contains control bytes")
		}
	}
	if !strings.HasPrefix(key, "/") {
		return errors.New("New electionKey must begin with '/' (etcd prefix convention)")
	}
	return nil
}

// IsLeader reports whether this replica currently believes it holds
// leadership. Eventually consistent — see
// [leaderelection.Elector.IsLeader] for the same caveat written
// long-hand.
func (e *Elector) IsLeader() bool {
	return e.leader.Load()
}

// Run blocks while attempting to acquire and hold leadership.
// Single-goroutine only — see [Elector] type docs.
//
// The loop attempts acquire, holds the term until the etcd session
// or caller ctx ends, runs OnAcquired with a leader-scoped ctx,
// drains it, runs OnLost, and re-enters the acquire loop. Returns
// when ctx cancels or [WithCallbackDrainTimeout] fires.
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

	var lastDrainTimeoutErr error
	for ctx.Err() == nil {
		termErr, drainTimedOut := e.runOnce(ctx, cb)
		if drainTimedOut {
			lastDrainTimeoutErr = termErr
			break
		}
		if termErr != nil {
			// An orderly shutdown of a non-leader replica (caller ctx
			// cancelled while Campaign blocks, or session create failing
			// on the cancelled ctx) surfaces as a context error. That is
			// expected steady-state teardown, not a fault, so log it at
			// Debug to avoid a spurious warn on every clean shutdown.
			if ctx.Err() != nil && isContextTermination(termErr) {
				e.logger.Debug("leader-election: term ended during shutdown",
					redact.String("election", e.electionKey),
					redact.String("identity", e.identity),
					redact.Error(termErr),
				)
			} else {
				e.logger.Warn("leader-election: term ended with error",
					redact.String("election", e.electionKey),
					redact.String("identity", e.identity),
					redact.Error(termErr),
				)
			}
		}
		if ctx.Err() != nil {
			break
		}
		// Backoff before re-acquiring. Honours ctx cancellation
		// during the wait so shutdown is prompt.
		select {
		case <-ctx.Done():
		case <-time.After(e.reacquireBackoff):
		}
	}

	if lastDrainTimeoutErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return errors.Join(ctxErr, lastDrainTimeoutErr)
		}
		return lastDrainTimeoutErr
	}
	return ctx.Err()
}

// runOnce performs one acquire/hold/drain cycle. Returns
// (termErr, drainTimedOut) — the second flag is hoisted out of
// termErr so Run can break out of the retry loop without parsing
// the error chain.
func (e *Elector) runOnce(ctx context.Context, cb leaderelection.Callbacks) (error, bool) {
	sess, elect, err := e.sessionFactory(ctx, e.leaseTTLSeconds, e.electionKey)
	if err != nil {
		return redact.WrapError("leader-election: session create", err), false
	}
	// Ensure session is always closed when this term ends. Close
	// failures only log — the lease will TTL out regardless.
	defer func() {
		if cerr := sess.Close(); cerr != nil {
			e.logger.Debug("leader-election: session close",
				redact.String("election", e.electionKey),
				redact.Error(cerr),
			)
		}
	}()

	if err := elect.Campaign(ctx, e.identity); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr, false
		}
		return redact.WrapError("leader-election: campaign", err), false
	}

	// We are leader.
	e.leader.Store(true)
	e.logger.Info("leader-election: acquired",
		redact.String("election", e.electionKey),
		redact.String("identity", e.identity),
	)

	// Build the leader ctx. It is cancelled when the session ends
	// (lease lost) OR the caller ctx is cancelled. The watcher
	// goroutine exits as soon as the leader ctx is cancelled by
	// either path.
	leaderCtx, leaderCancel := context.WithCancel(ctx)
	defer leaderCancel()
	go func() {
		select {
		case <-sess.Done():
			leaderCancel()
		case <-leaderCtx.Done():
		}
	}()

	// Run OnAcquired in its own goroutine so we can drain it under a
	// warn-ticker and optional timeout. Panic-recover before any other
	// work so an early panic still funnels onto cbDone.
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
			cb.OnAcquired(leaderCtx)
		}
	}()

	// Hold phase: wait for the term to end before arming the drain
	// watchdog. The watchdog (warn ticker + optional drain timeout)
	// governs the post-leadership cleanup window, NOT the steady-state
	// term — starting it at term start would emit spurious "still
	// draining" warnings, record full healthy terms as "drained", and
	// (with WithCallbackDrainTimeout) forcibly resign any healthy term
	// longer than the timeout. This mirrors the hold-then-drain shape in
	// pgadvisory.holdLeadership / redislock.holdLeadership / k8slease
	// (drain in OnStoppedLeading).
	//
	// If OnAcquired returns on its own while still leader, the term is
	// over with no drain accounting. Otherwise leaderCtx.Done() (lease
	// lost or caller ctx cancelled) ends the term and we drain.
	var drainResult callbackResult
	select {
	case drainResult = <-cbDone:
		// Callback returned during a healthy term — no drain window.
		// Clear the flag before OnLost so IsLeader observes false.
		e.leader.Store(false)
	case <-leaderCtx.Done():
		// Lease lost / parent cancelled: drop IsLeader *before* the
		// (possibly unbounded) callback drain so another replica that
		// has already campaigned successfully is not masked by a
		// still-true local flag. Mirrors pgadvisory / k8slease /
		// redislock.
		e.leader.Store(false)
		drainResult = e.awaitCallbackDrain(cbDone)
	}
	leaderCancel() // ensure leader ctx is cancelled before resign

	// leader flag already cleared above (before drain on the loss path,
	// and before OnLost on the happy path). Keep a defensive Store so
	// any future reorder still satisfies the kit contract that
	// IsLeader is false for OnLost.

	// Resign cleanly so peers do not wait out the lease TTL on a
	// planned shutdown. Use a detached release context so the resign
	// still runs when the caller ctx is already cancelled.
	resignCtx, cancel := detachedReleaseContext(ctx, 5*time.Second)
	defer cancel()
	if rerr := elect.Resign(resignCtx); rerr != nil {
		e.logger.Debug("leader-election: resign",
			redact.String("election", e.electionKey),
			redact.Error(rerr),
		)
	}

	e.logger.Info("leader-election: lost",
		redact.String("election", e.electionKey),
		redact.String("identity", e.identity),
	)

	lostErr := e.runOnLost(cb)
	termErr := joinTermErrors(lostErr, drainResult)
	return termErr, drainResult.timedOut
}

// isContextTermination reports whether a term error is just the caller
// ctx being cancelled or timing out — the expected end of every standby
// replica's term on orderly shutdown rather than a backend fault.
func isContextTermination(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func detachedReleaseContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), timeout)
}

// runOnLost invokes the user's OnLost callback under a panic guard so
// a buggy cleanup hook surfaces as an error from Run rather than
// crashing the process.
func (e *Elector) runOnLost(cb leaderelection.Callbacks) (err error) {
	if cb.OnLost == nil {
		return nil
	}
	defer func() {
		if rec := recover(); rec != nil {
			e.logger.Error("leader-election: OnLost callback panicked",
				redact.String("election", e.electionKey),
				redact.Panic(rec),
			)
			err = fmt.Errorf("leader-election: OnLost panic: %s", redact.PanicValue(rec))
		}
	}()
	cb.OnLost()
	return nil
}

// callbackResult mirrors the k8slease shape so cross-adapter
// debugging is symmetrical. panicValue captures a recover() value;
// timedOut signals that the configured drain timeout fired before
// OnAcquired returned.
type callbackResult struct {
	panicValue any
	timedOut   bool
}

// awaitCallbackDrain blocks until OnAcquired has returned (signalled
// via cbDone) OR the configured drain timeout fires. Emits a warn
// log + pending-drain metric every drainWarnTick while waiting so a
// stalled callback is operator-visible.
func (e *Elector) awaitCallbackDrain(cbDone <-chan callbackResult) callbackResult {
	start := time.Now()
	tick := e.drainWarnTick
	if tick <= 0 {
		tick = defaultDrainWarnTick
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
				e.metrics.observeDrainDuration(time.Since(start), e.electionKey, drainStateDrained)
			}
			return result
		case <-deadline:
			elapsed := time.Since(start)
			e.logger.Error("leader-election: OnAcquired callback drain timeout — orphan goroutine left running, orchestrator must restart process",
				redact.String("election", e.electionKey),
				slog.Duration("elapsed", elapsed),
				slog.Duration("timeout", e.drainTimeout),
			)
			if e.metrics != nil {
				e.metrics.observeDrainDuration(elapsed, e.electionKey, drainStateTimeout)
			}
			return callbackResult{timedOut: true}
		case <-ticker.C:
			elapsed := time.Since(start)
			e.logger.Warn("leader-election: OnAcquired callback still draining",
				redact.String("election", e.electionKey),
				slog.Duration("elapsed", elapsed),
			)
			if e.metrics != nil {
				e.metrics.observeDrainDuration(elapsed, e.electionKey, drainStatePending)
				e.metrics.observeDrainWarn(e.electionKey)
			}
		}
	}
}

func onAcquiredPanicError(rec any) error {
	return fmt.Errorf("leader-election: OnAcquired panic: %s", redact.PanicValue(rec))
}

// joinTermErrors collapses the per-term signals — OnLost callback
// error, OnAcquired panic, drain timeout — into a single error chain
// so callers see every relevant signal via errors.Is on the returned
// chain.
func joinTermErrors(lostErr error, drainResult callbackResult) error {
	var errs []error
	if lostErr != nil {
		errs = append(errs, lostErr)
	}
	if drainResult.panicValue != nil {
		errs = append(errs, onAcquiredPanicError(drainResult.panicValue))
	}
	if drainResult.timedOut {
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

// Compile-time guard that the Elector satisfies the kit's contract.
var _ leaderelection.Elector = (*Elector)(nil)
