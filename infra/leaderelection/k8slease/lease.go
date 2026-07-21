// Package k8slease implements [leaderelection.Elector] on top of
// Kubernetes coordination.k8s.io/v1 Lease objects via
// k8s.io/client-go/tools/leaderelection.
//
// One leader per (namespace, name) tuple across every replica that
// shares the same API server: the elector competes for a Lease object
// and renews it on the configured cadence. Renewal failure (network
// blip, API-server failover, lease forcibly taken by another holder)
// cancels the leader ctx so OnAcquired can drain before another
// replica is allowed to take over.
//
// Recommended when:
//
//   - The service already runs on Kubernetes and the operator wants
//     leader election to live in the same control plane as the
//     workload (no separate Postgres / Redis dependency to wire).
//   - kubectl-level visibility into who currently leads (kubectl get
//     lease -n <ns> <name>) is operationally useful.
//
// Fencing model: Lease-based locks rely on the API server's
// resourceVersion to detect concurrent updates, and on the leader
// holder's identity field for ownership. The renew deadline / lease
// duration window controls how long another replica must wait before
// it may forcibly acquire after the previous leader stops renewing.
// As with any TTL-based lock, a stalled leader (GC pause, kernel
// freeze) past the renew deadline opens a brief window where a second
// replica can become leader before the first notices it lost.
// Application-level fencing is required for work that must NEVER
// overlap; see [redislock] for the same caveat written long-hand.
//
// Heavy-SDK boundary: this module is the only place inside the kit
// that depends on k8s.io/client-go. Consumers that do not run on
// Kubernetes never import this package and never pull the dep
// transitively.
package k8slease

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	clientgoleaderelection "k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/leaderelection"
)

// Default lease / renew / retry intervals mirror the client-go
// upstream defaults (see k8s.io/client-go/tools/leaderelection). Keep
// them in sync if upstream changes them: the operator expectation in
// Kubernetes deployments is "kit acts like every other controller".
const (
	defaultLeaseDuration = 15 * time.Second
	defaultRenewDeadline = 10 * time.Second
	defaultRetryPeriod   = 2 * time.Second
	defaultDrainWarnTick = 30 * time.Second
)

// ErrCallbackDrainTimeout is returned by Run when
// [WithCallbackDrainTimeout] is configured and the OnAcquired
// callback did not return within the configured drain window after
// leadership ended. The orphan goroutine is left running — the
// orchestrator MUST treat this as a fatal signal and restart the
// process rather than retrying the elector in-place.
var ErrCallbackDrainTimeout = errors.New("leaderelection/k8slease: OnAcquired callback drain timed out")

// ErrLeadershipLost is returned by Run when client-go's one-shot
// LeaderElector.Run stops holding the Lease for a reason OTHER than
// caller ctx cancellation (renew failure: network blip, API-server
// failover, lease forcibly taken by a peer) and no OnStoppedLeading
// error was captured. Returning nil there would read to callers as a
// clean shutdown and they would stop retrying — contradicting the
// [leaderelection.Elector] contract ("returns when ctx cancels or an
// unrecoverable backend error occurs"). A retry loop (the kit's
// lifecycle.Runner restart policy, or a hand-rolled loop) should treat
// this as a recoverable signal to re-enter Run; a clean ctx-cancel
// shutdown returns ctx.Err() instead.
var ErrLeadershipLost = errors.New("leaderelection/k8slease: leadership lost (renew failure); Run should be retried")

// Elector is a [leaderelection.Elector] backed by a Kubernetes Lease.
//
// Concurrency: [Elector.Run] must be invoked from a single goroutine —
// a second Run on the same Elector would race the leader flag and call
// user callbacks out of order. [Elector.IsLeader] is safe for concurrent
// reads.
type Elector struct {
	client        kubernetes.Interface
	namespace     string
	name          string
	identity      string
	leaseDuration time.Duration
	renewDeadline time.Duration
	retryPeriod   time.Duration
	drainWarnTick time.Duration
	drainTimeout  time.Duration
	logger        *slog.Logger
	metrics       callbackDrainMetrics

	leader  atomic.Bool
	started atomic.Bool
}

// Option configures the Elector.
type Option func(*Elector)

// WithLeaseDuration sets the duration that non-leader replicas will
// wait before forcibly acquiring leadership after the previous holder
// stops renewing. Default: 15 seconds (mirrors client-go upstream).
//
// Must be strictly greater than [WithRenewDeadline]; otherwise a
// leader renewing on time can still appear stale to peers. New panics
// on invalid combinations.
func WithLeaseDuration(d time.Duration) Option {
	if d <= 0 {
		panic("leaderelection/k8slease: WithLeaseDuration requires a positive duration")
	}
	return func(e *Elector) { e.leaseDuration = d }
}

// WithRenewDeadline sets the deadline for renewing the Lease. The
// leader must successfully renew within this window or it relinquishes
// leadership. Default: 10 seconds (mirrors client-go upstream).
//
// Must be strictly less than [WithLeaseDuration] and strictly greater
// than [WithRetryPeriod]; see client-go's leaderelection.NewLeaderElector
// for the same constraint. New panics on invalid combinations.
func WithRenewDeadline(d time.Duration) Option {
	if d <= 0 {
		panic("leaderelection/k8slease: WithRenewDeadline requires a positive duration")
	}
	return func(e *Elector) { e.renewDeadline = d }
}

// WithRetryPeriod sets how often the elector retries acquire / renew
// against the API server. Default: 2 seconds (mirrors client-go
// upstream).
//
// Must be strictly less than [WithRenewDeadline] so a single transient
// API-server error does not exhaust the renew window. New panics on
// invalid combinations.
func WithRetryPeriod(d time.Duration) Option {
	if d <= 0 {
		panic("leaderelection/k8slease: WithRetryPeriod requires a positive duration")
	}
	return func(e *Elector) { e.retryPeriod = d }
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
// watchdog. When this option is set, [New] validates the Lease
// (namespace, name) values against [promutil.ValidateStaticLabelValue]
// so a misconfigured caller fails fast at construction rather than
// producing silent metric label injection.
//
// Passing nil panics so that "metrics enabled but unwired" never
// degrades into a silent no-op — omit the option entirely to opt out.
func WithMetrics(m *Metrics) Option {
	if m == nil {
		panic("leaderelection/k8slease: WithMetrics requires non-nil metrics (omit the option for no metrics)")
	}
	return func(e *Elector) { e.metrics = m }
}

// WithCallbackDrainWarnInterval overrides the cadence at which the
// elector logs a warning and records a pending-drain metric while
// waiting for [leaderelection.Callbacks.OnAcquired] to return after
// leadership ended. Default: 30 seconds (matches pgadvisory / redislock).
func WithCallbackDrainWarnInterval(d time.Duration) Option {
	if d <= 0 {
		panic("leaderelection/k8slease: WithCallbackDrainWarnInterval requires a positive duration")
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
// Use this option when an external orchestrator (Kubernetes pod
// restart) will SIGKILL the process within a bounded grace window
// anyway and the kit should record the stalled-callback evidence
// first instead of being silently terminated.
func WithCallbackDrainTimeout(d time.Duration) Option {
	if d <= 0 {
		panic("leaderelection/k8slease: WithCallbackDrainTimeout requires a positive duration")
	}
	return func(e *Elector) { e.drainTimeout = d }
}

// New constructs an Elector that competes for a Kubernetes Lease at
// (namespace, name) on behalf of `identity`. Every replica wiring this
// elector MUST pass a distinct `identity` (typically the pod name)
// because the Lease ownership field uses it as the strict per-holder
// token — two replicas sharing an identity cannot tell themselves
// apart and would race the leader flag inside this process.
//
// Panics on invalid argument shapes (nil client, empty namespace /
// name / identity, nil option, invalid option combinations) so the
// misconfiguration fails fast at startup.
func New(client kubernetes.Interface, namespace, name, identity string, opts ...Option) *Elector {
	if client == nil {
		panic("leaderelection/k8slease: New client must not be nil")
	}
	if namespace == "" {
		panic("leaderelection/k8slease: New namespace must not be empty")
	}
	if name == "" {
		panic("leaderelection/k8slease: New name must not be empty")
	}
	if identity == "" {
		panic("leaderelection/k8slease: New identity must not be empty — use the pod name (POD_NAME env) or another per-replica unique value")
	}
	e := &Elector{
		client:        client,
		namespace:     namespace,
		name:          name,
		identity:      identity,
		leaseDuration: defaultLeaseDuration,
		renewDeadline: defaultRenewDeadline,
		retryPeriod:   defaultRetryPeriod,
		drainWarnTick: defaultDrainWarnTick,
		logger:        slog.Default(),
	}
	for _, o := range opts {
		if o == nil {
			panic("leaderelection/k8slease: New option must not be nil")
		}
		o(e)
	}
	validateDurations(e)
	if e.metrics != nil {
		validateMetricLabel("namespace", e.namespace)
		validateMetricLabel("name", e.name)
	}
	return e
}

// validateDurations enforces client-go's relational constraints
// between leaseDuration, renewDeadline, and retryPeriod. Mirrors
// k8s.io/client-go/tools/leaderelection.NewLeaderElector's runtime
// validation but happens at our construction time so misconfiguration
// surfaces during boot rather than inside the first Run.
func validateDurations(e *Elector) {
	if e.leaseDuration <= e.renewDeadline {
		panic(fmt.Sprintf(
			"leaderelection/k8slease: New requires WithLeaseDuration (%s) strictly greater than WithRenewDeadline (%s)",
			e.leaseDuration, e.renewDeadline,
		))
	}
	if e.renewDeadline <= e.retryPeriod {
		panic(fmt.Sprintf(
			"leaderelection/k8slease: New requires WithRenewDeadline (%s) strictly greater than WithRetryPeriod (%s)",
			e.renewDeadline, e.retryPeriod,
		))
	}
}

// IsLeader reports whether this replica currently believes it holds
// leadership. Eventually consistent — see
// [leaderelection.Elector.IsLeader] for the same caveat.
func (e *Elector) IsLeader() bool {
	return e.leader.Load()
}

// Run blocks while trying to acquire and hold leadership. Single-goroutine
// only — see [Elector] type docs.
//
// Unlike [pgadvisory] / [redislock] which loop the acquire path
// inside the kit, client-go's LeaderElector already owns the
// acquire / renew / retry loop. Run delegates to LeaderElector.Run
// and surfaces the leader-state transitions to
// [leaderelection.Callbacks].
func (e *Elector) Run(ctx context.Context, cb leaderelection.Callbacks) error {
	if ctx == nil {
		return errors.New("leader-election: Run requires a non-nil context")
	}
	if !e.started.CompareAndSwap(false, true) {
		return errors.New("leader-election: Run already invoked concurrently on this Elector — a second concurrent Run would race the leader flag and call OnAcquired / OnLost out of order")
	}
	// Clear started on every return path once this goroutine owns the
	// CAS. client-go's LeaderElector.Run is one-shot; the package docs
	// tell callers to wrap Run in a retry loop (lifecycle.Runner does
	// this via its restart policy), so a transient API-server blip that
	// ends a term must NOT permanently poison the Elector with a stuck
	// started flag. Single-goroutine-Run is still enforced: a *second
	// concurrent* Run loses the CAS above and is rejected.
	defer e.started.Store(false)

	lock := &resourcelock.LeaseLock{
		LeaseMeta: metav1.ObjectMeta{
			Namespace: e.namespace,
			Name:      e.name,
		},
		Client: e.client.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{
			Identity: e.identity,
		},
	}

	// tm coordinates the two halves of a single client-go leadership
	// term — OnStartedLeading (its own goroutine) and OnStoppedLeading
	// (Run's goroutine, deferred) — so the claim/skip decision is
	// serialized under one lock. This closes the TOCTOU race between
	// OnStartedLeading checking leaderCtx and actually claiming the term
	// (see [term] and [Elector.onStartedLeading]). Its cbDone channel
	// also funnels OnAcquired completion (panic-aware) so OnLost can only
	// run after the user callback returned, mirroring the explicit drain
	// enforcement in pgadvisory / redislock.
	tm := newTerm()
	var lostErrSlot atomic.Pointer[error]

	config := clientgoleaderelection.LeaderElectionConfig{
		Lock:          lock,
		LeaseDuration: e.leaseDuration,
		RenewDeadline: e.renewDeadline,
		RetryPeriod:   e.retryPeriod,
		// ReleaseOnCancel: cleanly hand the Lease back to peers when
		// the caller ctx cancels. Without this, an orderly shutdown
		// still forces peers to wait out the full lease duration.
		ReleaseOnCancel: true,
		Name:            fmt.Sprintf("%s/%s", e.namespace, e.name),
		Callbacks: clientgoleaderelection.LeaderCallbacks{
			OnStartedLeading: func(leaderCtx context.Context) {
				e.onStartedLeading(leaderCtx, tm, cb)
			},
			OnStoppedLeading: func() {
				if !e.onStoppedLeading(tm, cb) {
					return
				}
				// Wait for the OnAcquired goroutine to drain, then call
				// OnLost synchronously. awaitCallbackDrain handles warn
				// ticks, optional drain-timeout, and terminal metrics.
				drainResult := e.awaitCallbackDrain(tm.cbDone)
				// Re-assert leader=false AFTER the OnAcquired goroutine
				// has drained. onStartedLeading may still race past
				// claim() and Store(true) after stop() if it was
				// preempted between claim and the true-store; without
				// this post-drain clear, IsLeader() sticks true forever.
				e.leader.Store(false)
				lostErr := e.runOnLost(cb)
				if perr := joinStoppedLeadingErrors(lostErr, drainResult); perr != nil {
					lostErrSlot.Store(&perr)
				}
			},
			OnNewLeader: func(identity string) {
				if identity == e.identity {
					return
				}
				e.logger.Info("leader-election: another replica is leader",
					redact.String("namespace", e.namespace),
					redact.String("name", e.name),
					redact.String("leader", identity),
				)
			},
		},
	}

	le, err := clientgoleaderelection.NewLeaderElector(config)
	if err != nil {
		// The deferred started reset (above) restores a fresh Elector
		// for the unit-test surface that asserts retry after a
		// construction-time error.
		return redact.WrapError("leader-election: NewLeaderElector", err)
	}

	// client-go's Run blocks until ctx cancels or the leader stops
	// holding the Lease. It does NOT loop re-acquire — the upstream
	// contract is one-shot, so the kit's Run also returns once
	// LeaderElector.Run returns. Callers that want continuous
	// re-election should wrap Run in their own retry loop (the kit's
	// lifecycle.Runner handles this naturally via its restart policy).
	le.Run(ctx)

	// Guard (b): LeaderElector.Run has returned, which means its deferred
	// OnStoppedLeading already ran (and, when the term was acquired,
	// drained the OnAcquired goroutine via awaitCallbackDrain). The one
	// half client-go does NOT join is the `go OnStartedLeading` goroutine
	// when OnStoppedLeading skipped the drain (term never acquired). That
	// goroutine may still be pending; the term coordinator already makes
	// it refuse to claim leadership (it observes term.stopped), but we
	// also wait for it to settle here so the invariant is total: when Run
	// returns, OnAcquired is never still about to fire. settleStarted is
	// a no-op when OnStoppedLeading already consumed cbDone.
	e.settleStarted(tm)

	// Surface any error captured during OnStoppedLeading.
	var lostErr error
	if errPtr := lostErrSlot.Load(); errPtr != nil {
		lostErr = *errPtr
	}
	return computeRunResult(lostErr, ctx.Err())
}

// computeRunResult turns the two independent termination signals — the
// error captured in OnStoppedLeading (OnLost failure / OnAcquired panic
// / drain timeout) and the caller ctx error — into Run's return value.
//
//   - ctx cancelled, no captured error: clean caller-initiated shutdown,
//     return ctx.Err().
//   - ctx cancelled AND a captured error: join both so callers see every
//     relevant signal via errors.Is.
//   - ctx live, captured error present: leadership ended with a concrete
//     failure; return it as-is (it already describes the loss).
//   - ctx live, no captured error: client-go's one-shot Run stopped
//     holding the Lease for a renew-failure reason. Return
//     [ErrLeadershipLost] rather than nil so a retry loop does not read
//     the loss as a clean shutdown and stop re-electing.
func computeRunResult(lostErr, ctxErr error) error {
	if ctxErr != nil {
		if lostErr != nil {
			return errors.Join(ctxErr, lostErr)
		}
		return ctxErr
	}
	if lostErr != nil {
		return lostErr
	}
	return ErrLeadershipLost
}

// term coordinates the two halves of a single client-go leadership term
// so the OnAcquired/OnLost balance and the IsLeader()==false-on-return
// invariant survive every scheduling order. client-go's Run is roughly:
//
//	defer OnStoppedLeading()
//	...
//	go OnStartedLeading(leaderCtx); le.renew(leaderCtx)
//
// OnStartedLeading runs in its own goroutine while OnStoppedLeading runs
// (deferred) in Run's goroutine after renew returns and leaderCtx is
// cancelled. Without a shared lock there is a TOCTOU window: the started
// goroutine can pass a leaderCtx.Err()==nil check, get preempted, let
// OnStoppedLeading observe "never acquired" and skip drain+OnLost, then
// resume and store leader=true / call OnAcquired after Run returned.
//
// The mutex makes the claim (onStartedLeading) and the stop (onStopped-
// Leading) mutually exclusive on the acquired/stopped flags so exactly
// one of two consistent outcomes happens:
//
//   - claim wins first: acquired=true; stop then sees acquired and
//     drains the callback + runs OnLost.
//   - stop wins first: stopped=true; the late claim sees stopped and
//     refuses (leader stays false, OnAcquired never runs).
//
// cbDone (buffered, size 1) funnels OnAcquired completion (panic-aware)
// to awaitCallbackDrain; it is always sent exactly once by
// onStartedLeading even on the skip path.
type term struct {
	mu       sync.Mutex
	launched bool
	acquired bool
	stopped  bool
	cbDone   chan callbackResult
}

func newTerm() *term {
	return &term{cbDone: make(chan callbackResult, 1)}
}

// claim attempts to mark the term acquired. It records that the started
// goroutine was launched (so Run's settle path knows cbDone will be
// signalled) and returns false — refusing the claim — when the term has
// already stopped, which is the late-OnStartedLeading case that must not
// resurrect leadership.
func (t *term) claim() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.launched = true
	if t.stopped {
		return false
	}
	t.acquired = true
	return true
}

// markLaunched records that the started goroutine ran far enough to
// guarantee its deferred cbDone send will happen, even on the fast
// leaderCtx-already-cancelled skip path that returns before claim().
func (t *term) markLaunched() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.launched = true
}

// settleState snapshots, under the lock, whether the started goroutine
// was launched and whether the term was acquired, so Run can decide if
// it must wait for a pending cbDone send without risking a deadlock when
// the goroutine was never launched (acquire failed before scheduling it).
func (t *term) settleState() (launched, acquired bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.launched, t.acquired
}

// stop marks the term stopped and reports whether OnStartedLeading had
// already claimed it. A true result means there is an acquired term to
// drain and an OnLost to run; false means leadership was never truly
// taken and OnStoppedLeading must skip both.
func (t *term) stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
	return t.acquired
}

// didAcquire reports whether the term was claimed by OnStartedLeading.
// Used by Run's settle path and by tests.
func (t *term) didAcquire() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.acquired
}

// onStartedLeading runs the OnAcquired half of a leadership term. It is
// invoked by client-go inside its own goroutine
// (go OnStartedLeading(ctx); le.renew(ctx)), so it can be scheduled
// AFTER le.renew has already returned and OnStoppedLeading has run — for
// example when the caller ctx was already cancelled at the instant
// acquire succeeded, or on an immediate renew failure.
//
// Two guards keep IsLeader() from sticking true forever and keep
// OnAcquired from running for an already-over term:
//
//   - A fast leaderCtx.Err() check skips the obvious case where the term
//     was cancelled before this goroutine was scheduled.
//   - term.claim() closes the TOCTOU window: if OnStoppedLeading already
//     stopped the term (even after the ctx check passed), claim returns
//     false and this goroutine makes no observable claim — leader stays
//     false, the term stays not-acquired (so OnStoppedLeading correctly
//     skips OnLost), and OnAcquired is not called.
//
// cbDone is always signalled (via defer) so a concurrent
// awaitCallbackDrain never blocks.
func (e *Elector) onStartedLeading(
	leaderCtx context.Context,
	tm *term,
	cb leaderelection.Callbacks,
) {
	// Set up the panic-recover defer FIRST so any panic between here and
	// cb.OnAcquired (including a hypothetical panic inside slog.Info)
	// still funnels completion onto cbDone. Without this, a panic before
	// the defer ran would leave OnStoppedLeading's awaitCallbackDrain
	// blocked until drainTimeout (or forever).
	var result callbackResult
	defer func() {
		if rec := recover(); rec != nil {
			result.panicValue = rec
		}
		tm.cbDone <- result
	}()
	// Leadership already ended before this goroutine was scheduled — do
	// not claim a term that is over. (Fast path; claim() below is the
	// authoritative guard against the TOCTOU window.) markLaunched keeps
	// Run's settle path correct: this goroutine WILL send on cbDone via
	// the defer above, so settleStarted may safely wait for it.
	if leaderCtx.Err() != nil {
		tm.markLaunched()
		return
	}
	// Authoritative guard: refuse to claim if OnStoppedLeading already
	// stopped the term. claim() and stop() are serialized under the term
	// lock, so the late goroutine can never resurrect leadership.
	if !tm.claim() {
		return
	}
	e.leader.Store(true)
	e.logger.Info("leader-election: acquired",
		redact.String("namespace", e.namespace),
		redact.String("name", e.name),
		redact.String("identity", e.identity),
	)
	if cb.OnAcquired != nil {
		cb.OnAcquired(leaderCtx)
	}
}

// onStoppedLeading runs the stop half of a leadership term in Run's
// goroutine (client-go invokes it via a deferred OnStoppedLeading). It
// stores leader=false under the term lock — ordering the write with
// onStartedLeading's leader=true store so a late claim cannot leave
// IsLeader() stuck true — logs the loss, and reports whether the term
// had actually been acquired.
//
// A true return tells Run's OnStoppedLeading closure that there is an
// acquired term: it must drain the OnAcquired callback and run OnLost. A
// false return means leadership was never truly taken (the started
// goroutine never claimed, or will refuse to because the term is now
// stopped), so OnLost is skipped per the kit contract.
func (e *Elector) onStoppedLeading(tm *term, _ leaderelection.Callbacks) bool {
	acquired := tm.stop()
	e.leader.Store(false)
	e.logger.Info("leader-election: lost",
		redact.String("namespace", e.namespace),
		redact.String("name", e.name),
		redact.String("identity", e.identity),
	)
	return acquired
}

// settleStarted waits for a late OnStartedLeading goroutine to finish
// after LeaderElector.Run returned, but only when OnStoppedLeading did
// not already drain it (term not acquired) AND the goroutine was in fact
// launched. The started goroutine, if still pending, observes
// term.stopped and refuses to claim (leader stays false), then sends on
// cbDone via its defer; receiving that send guarantees no OnAcquired is
// still about to fire once Run returns.
//
// The launched check avoids a deadlock: client-go only does
// `go OnStartedLeading` when acquire succeeds, so a ctx-cancelled-before-
// acquire shutdown never schedules the goroutine and nothing would ever
// send on cbDone. When the term was acquired, OnStoppedLeading's
// awaitCallbackDrain already consumed cbDone, so there is nothing to
// settle and this returns immediately.
func (e *Elector) settleStarted(tm *term) {
	launched, acquired := tm.settleState()
	if acquired || !launched {
		return
	}
	<-tm.cbDone
}

// runOnLost invokes the user's OnLost callback under a panic guard so
// a buggy cleanup hook surfaces as an error from Run rather than
// crashing the process inside the client-go goroutine.
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
				redact.String("namespace", e.namespace),
				redact.String("name", e.name),
				redact.Panic(rec),
			)
			err = fmt.Errorf("leader-election: OnLost panic: %s", redact.PanicValue(rec))
		}
	}()
	cb.OnLost()
	return nil
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
// callbackResult with timedOut=true. Run lifts this into
// [ErrCallbackDrainTimeout] for the caller boundary.
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
				e.metrics.observeDrainDuration(time.Since(start), e.namespace, e.name, drainStateDrained)
			}
			return result
		case <-deadline:
			elapsed := time.Since(start)
			logger := e.logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.Error("leader-election: OnAcquired callback drain timeout — orphan goroutine left running, orchestrator must restart process",
				redact.String("namespace", e.namespace),
				redact.String("name", e.name),
				slog.Duration("elapsed", elapsed),
				slog.Duration("timeout", e.drainTimeout),
			)
			if e.metrics != nil {
				e.metrics.observeDrainDuration(elapsed, e.namespace, e.name, drainStateTimeout)
			}
			return callbackResult{timedOut: true}
		case <-ticker.C:
			elapsed := time.Since(start)
			logger := e.logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.Warn("leader-election: OnAcquired callback still draining",
				redact.String("namespace", e.namespace),
				redact.String("name", e.name),
				slog.Duration("elapsed", elapsed),
			)
			if e.metrics != nil {
				e.metrics.observeDrainDuration(elapsed, e.namespace, e.name, drainStatePending)
				e.metrics.observeDrainWarn(e.namespace, e.name)
			}
		}
	}
}

func onAcquiredPanicError(rec any) error {
	return fmt.Errorf("leader-election: OnAcquired panic: %s", redact.PanicValue(rec))
}

// joinStoppedLeadingErrors collapses the three independently-derivable
// termination signals captured in OnStoppedLeading — an OnLost callback
// error, an OnAcquired panic, and a callback-drain timeout — into a
// single error chain. The caller sees every relevant signal via
// errors.Is on the returned chain instead of having later signals
// overwrite earlier ones (which was the wave 127 behaviour). Returns
// nil when no signal fired.
func joinStoppedLeadingErrors(lostErr error, drainResult callbackResult) error {
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

// Compile-time guard that the upstream Lease type is the one we
// reference; prevents accidental drift if client-go renames the GVK.
var _ = coordinationv1.Lease{}
