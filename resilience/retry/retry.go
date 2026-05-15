// Package retry provides generic retry and backoff utilities for operations
// that may fail transiently. It consolidates the duplicated backoff patterns
// used across redis, messaging, and notification delivery.
package retry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"time"

	"github.com/cenkalti/backoff/v5"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/redact"
)

// Policy configures retry behaviour.
type Policy struct {
	// MaxRetries is the maximum number of retry attempts per stability cycle.
	// Zero means no retries (run once). Negative means unlimited.
	//
	// IMPORTANT: When StableReset is also set, MaxRetries applies per cycle —
	// each time the function runs for at least StableReset before failing,
	// the attempt counter resets, allowing another MaxRetries attempts. This
	// means the total number of attempts across all cycles is unbounded.
	// For a hard total-attempt cap regardless of stability, do not set
	// StableReset — or use an external counter via OnRetry.
	MaxRetries int

	// BaseDelay is the initial delay before the first retry.
	BaseDelay time.Duration

	// MaxDelay caps the computed delay.
	MaxDelay time.Duration

	// Factor is the exponential backoff multiplier (e.g. 2.0 for doubling).
	Factor float64

	// Jitter adds ±Jitter fraction of randomness to each delay.
	// 0.25 means ±25%. Zero disables jitter.
	Jitter float64

	// StableReset (also known as "stability detection threshold") resets the
	// delay to BaseDelay when the function ran for at least this duration
	// before failing. This prevents escalating backoff for long-running
	// workers that fail after significant stable operation.
	// Zero disables stability detection.
	//
	// Note: in bounded retry (Do/DoWith), StableReset also resets the
	// attempt counter, effectively allowing unlimited retries as long as
	// each run lasts at least StableReset. This is by design for worker
	// loops (Loop). If you need a hard attempt cap, do not set StableReset.
	StableReset time.Duration

	// RetryIf determines whether a given error should be retried.
	// When nil, all errors are considered retryable.
	RetryIf func(err error) bool

	// OnRetry is called before sleeping between retries.
	// attempt is 1-based (first retry attempt is 1) within the current
	// stability cycle. When StableReset fires, the counter resets to 1.
	// For total-attempt tracking across cycles, use an external counter.
	OnRetry func(err error, attempt int, delay time.Duration)

	// MaxElapsedTime, if > 0, aborts the retry loop once the total
	// wall-clock time spent since the first attempt reaches this limit.
	// Use this to bound retries by SLO budget rather than just attempt
	// count. Zero disables the cap (only MaxRetries applies).
	MaxElapsedTime time.Duration

	// DelayOverride, if set, can replace the computed exponential delay
	// based on the failing error — typically used to honor a server's
	// Retry-After header. Return zero from the callback to fall back
	// to the policy's exponential delay.
	DelayOverride func(err error) time.Duration
}

// DefaultPolicy returns a sensible default: 3 retries, 1s base, 30s max, 2x factor,
// ±25% jitter, and a RetryIf predicate that skips apperror.Permanent errors.
//
// The RetryIf default matters: without it (nil), every error — including
// validation failures, permission denials, and explicitly permanent errors —
// would be retried, which is rarely correct for a generic helper. Callers
// that genuinely want "retry every error" must pass WithRetryIf(nil) or a
// custom predicate.
func DefaultPolicy() Policy {
	return Policy{
		MaxRetries: 3,
		BaseDelay:  1 * time.Second,
		MaxDelay:   30 * time.Second,
		Factor:     2.0,
		Jitter:     0.25,
		RetryIf:    RetryIfNotPermanent,
	}
}

// WorkerPolicy returns a policy tuned for long-running worker loops: unlimited retries,
// stability detection, matching the existing redis/messaging patterns.
func WorkerPolicy() Policy {
	return Policy{
		MaxRetries:  -1, // unlimited
		BaseDelay:   3 * time.Second,
		MaxDelay:    60 * time.Second,
		Factor:      2.0,
		Jitter:      0.25,
		StableReset: 30 * time.Second,
	}
}

// Option configures a Policy.
type Option func(*Policy)

// WithMaxRetries sets the maximum retry attempts.
func WithMaxRetries(n int) Option { return func(p *Policy) { p.MaxRetries = n } }

// WithBaseDelay sets the initial backoff delay.
//
// FR-087 [MED]: panics on d <= 0. A zero base delay turns the
// retry loop into a tight CPU-burning spin; a negative one is
// always a wiring bug.
func WithBaseDelay(d time.Duration) Option {
	if d <= 0 {
		panic("retry: WithBaseDelay requires d > 0")
	}
	return func(p *Policy) { p.BaseDelay = d }
}

// WithMaxDelay sets the maximum backoff delay.
//
// FR-087 [MED]: panics on d <= 0. A non-positive cap silently
// disables the cap and lets exponential backoff exceed any sane
// bound.
func WithMaxDelay(d time.Duration) Option {
	if d <= 0 {
		panic("retry: WithMaxDelay requires d > 0")
	}
	return func(p *Policy) { p.MaxDelay = d }
}

// WithFactor sets the exponential backoff multiplier.
//
// FR-087 [MED]: panics on f < 1. A factor below 1 produces a
// shrinking series (the second retry sleeps less than the first),
// which defeats the backoff and is always a wiring bug.
func WithFactor(f float64) Option {
	if f < 1 {
		panic("retry: WithFactor requires f >= 1")
	}
	return func(p *Policy) { p.Factor = f }
}

// WithMaxElapsedTime aborts retries once cumulative wall-clock time
// reaches d. Zero disables the cap.
func WithMaxElapsedTime(d time.Duration) Option {
	if d < 0 {
		panic("retry: WithMaxElapsedTime requires d >= 0")
	}
	return func(p *Policy) { p.MaxElapsedTime = d }
}

// WithDelayOverride sets a callback that can override the computed
// exponential delay based on the failing error — typically used for
// HTTP Retry-After headers. Return zero to use the policy's default.
func WithDelayOverride(fn func(err error) time.Duration) Option {
	return func(p *Policy) { p.DelayOverride = fn }
}

// WithJitter sets the jitter fraction. Must be in [0, 1]; values outside
// the range are panic-on-construction (matches WithFactor / WithMaxDelay).
// Silent clamping was inconsistent with [Policy.Validate], which rejects
// the same input on the struct-literal path.
func WithJitter(f float64) Option {
	if f < 0 || f > 1 {
		panic("retry: WithJitter requires 0 <= f <= 1")
	}
	return func(p *Policy) { p.Jitter = f }
}

// WithStableReset enables stability detection: if the function runs for at
// least d before failing, the delay resets to BaseDelay.
func WithStableReset(d time.Duration) Option {
	if d < 0 {
		panic("retry: WithStableReset requires d >= 0")
	}
	return func(p *Policy) { p.StableReset = d }
}

// WithRetryIf sets a predicate that decides whether an error should be retried.
func WithRetryIf(fn func(err error) bool) Option { return func(p *Policy) { p.RetryIf = fn } }

// WithOnRetry registers a callback invoked before each retry delay.
func WithOnRetry(fn func(err error, attempt int, delay time.Duration)) Option {
	return func(p *Policy) { p.OnRetry = fn }
}

// RetryIfNotPermanent returns false for apperror.Permanent errors.
// This is a convenience predicate for WithRetryIf.
func RetryIfNotPermanent(err error) bool {
	return !apperror.IsPermanent(err)
}

// Do executes fn and retries on error according to the given options.
// The default policy is DefaultPolicy(); options override individual fields.
// Returns the last error if all retries are exhausted or ctx is cancelled.
//
// Note: when StableReset and MaxRetries are both set, the attempt counter
// resets after a stable run, effectively allowing more than MaxRetries total
// attempts across stability cycles. Use MaxRetries alone for a firm cap.
func Do(ctx context.Context, fn func(ctx context.Context) error, opts ...Option) error {
	if fn == nil {
		panic("retry: Do: Do requires a non-nil fn")
	}
	p := DefaultPolicy()
	for _, o := range opts {
		if o == nil {
			panic("retry: Do: Do option must not be nil")
		}
		o(&p)
	}
	return doWithPolicy(ctx, p, fn)
}

// DoWith executes fn using a specific base policy (overridden by opts).
func DoWith(ctx context.Context, base Policy, fn func(ctx context.Context) error, opts ...Option) error {
	if fn == nil {
		panic("retry: DoWith requires a non-nil fn")
	}
	for _, o := range opts {
		if o == nil {
			panic("retry: DoWith option must not be nil")
		}
		o(&base)
	}
	return doWithPolicy(ctx, base, fn)
}

// Loop runs fn in an infinite restart loop with exponential backoff, logging
// errors between restarts. Blocks until ctx is cancelled.
// Uses WorkerPolicy() as default; options override individual fields.
func Loop(ctx context.Context, logger *slog.Logger, component string, fn func(ctx context.Context) error, opts ...Option) {
	if fn == nil {
		panic("retry: Loop requires a non-nil fn")
	}
	if logger == nil {
		logger = slog.Default()
	}
	p := WorkerPolicy()
	for _, o := range opts {
		if o == nil {
			panic("retry: Loop option must not be nil")
		}
		o(&p)
	}
	mustValidatePolicy("retry: Loop policy", p)

	bo := newBackOff(p)
	attempt := 0
	for {
		// Check ctx before invoking fn to avoid one extra execution
		// after the timer fires but ctx is already cancelled.
		if ctx.Err() != nil {
			return
		}

		start := time.Now()
		err := callLoopFunc(fn, ctx)

		// fn's err takes precedence over ctx.Err: if fn failed at the
		// same instant ctx was cancelled, log the business error before
		// exiting rather than silently swallowing it.
		if err != nil && ctx.Err() != nil {
			logger.Error(component+" stopped during shutdown",
				"error", err,
			)
			return
		}
		if ctx.Err() != nil {
			return
		}

		// Workers that return nil mean "graceful completion" — Loop should
		// exit, not infinitely restart. Without this guard a nil-returning
		// worker burns the full retry/backoff sequence with nil errors and
		// hands nil to RetryIf predicates that don't expect it.
		if err == nil {
			return
		}

		if p.RetryIf != nil && !callRetryIf(logger, component, p.RetryIf, err) {
			logger.Error(component+" stopped with non-retryable error", "error", err)
			return
		}

		if p.StableReset > 0 && time.Since(start) >= p.StableReset {
			bo.Reset()
			attempt = 0
		}

		if p.MaxRetries >= 0 && attempt >= p.MaxRetries {
			logger.Error(component+" max retries exhausted, stopping",
				"attempts", attempt, "max", p.MaxRetries)
			return
		}

		wait := bo.NextBackOff()
		if p.DelayOverride != nil {
			if override := callDelayOverride(logger, component, p.DelayOverride, err); override > 0 {
				wait = override
			}
		}

		if p.OnRetry != nil {
			callOnRetry(logger, component, p.OnRetry, err, attempt+1, wait)
		}

		logger.Warn(component+" stopped, restarting",
			"error", err,
			"restart_delay", wait,
			"attempt", attempt+1,
		)

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		attempt++
	}
}

func callLoopFunc(fn func(context.Context) error, ctx context.Context) (err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("retry: worker panic: %s", redact.PanicValue(rec))
		}
	}()
	return fn(ctx)
}

func callRetryIf(logger *slog.Logger, component string, fn func(error) bool, err error) (retry bool) {
	defer func() {
		if rec := recover(); rec != nil {
			logger.Error(component+" RetryIf callback panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			retry = false
		}
	}()
	return fn(err)
}

func callDelayOverride(logger *slog.Logger, component string, fn func(error) time.Duration, err error) (delay time.Duration) {
	defer func() {
		if rec := recover(); rec != nil {
			logger.Error(component+" DelayOverride callback panicked",
				redact.Panic(rec),
				"stack", string(debug.Stack()),
			)
			delay = 0
		}
	}()
	return fn(err)
}

func callOnRetry(logger *slog.Logger, component string, fn func(error, int, time.Duration), err error, attempt int, delay time.Duration) {
	defer func() {
		if rec := recover(); rec != nil {
			logger.Error(component+" OnRetry callback panicked",
				redact.Panic(rec),
				"attempt", attempt,
				"stack", string(debug.Stack()),
			)
		}
	}()
	fn(err, attempt, delay)
}

// Delay computes the backoff delay for a given attempt using the policy.
// Attempt 0 returns BaseDelay, attempt 1 returns BaseDelay*Factor, etc.
// For attempts beyond the backoff sequence, MaxDelay is returned.
// Negative attempts are clamped to 0.
func (p Policy) Delay(attempt int) time.Duration {
	mustValidatePolicy("retry: Policy.Delay", p)
	if attempt < 0 {
		attempt = 0
	}
	bo := newBackOff(p)
	var wait time.Duration
	for i := 0; i <= attempt; i++ {
		wait = bo.NextBackOff()
	}
	if wait == 0 {
		return p.BaseDelay
	}
	return wait
}

func doWithPolicy(ctx context.Context, p Policy, fn func(ctx context.Context) error) error {
	mustValidatePolicy("retry: Do policy", p)
	bo := newBackOff(p)
	attempt := 0
	loopStart := time.Now()

	for {
		// Honor a pre-cancelled ctx — otherwise the caller pays one full
		// fn() invocation against an already-dead context.
		if err := ctx.Err(); err != nil {
			return err
		}

		start := time.Now()
		err := fn(ctx)
		if err == nil {
			// fn succeeded — only surface a cancelled ctx if it
			// fired after the successful return. Otherwise nil.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			return nil
		}
		// fn returned a real error: it takes precedence over ctx.Err().
		// Joining preserves both signals for callers that care to inspect.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return errors.Join(err, ctxErr)
		}
		if p.RetryIf != nil && !callRetryIf(slog.Default(), "retry", p.RetryIf, err) {
			return err
		}

		// Bound by total wall-clock so retries don't outlast the caller's
		// SLO budget even when MaxRetries is generous.
		if p.MaxElapsedTime > 0 && time.Since(loopStart) >= p.MaxElapsedTime {
			return err
		}

		// Check StableReset before MaxRetries so a stable run resets the
		// counter — consistent with the Loop path.
		if p.StableReset > 0 && time.Since(start) >= p.StableReset {
			bo.Reset()
			attempt = 0
		}

		if p.MaxRetries >= 0 && attempt >= p.MaxRetries {
			return err
		}

		wait := bo.NextBackOff()
		if p.DelayOverride != nil {
			if override := callDelayOverride(slog.Default(), "retry", p.DelayOverride, err); override > 0 {
				wait = override
			}
		}

		if p.OnRetry != nil {
			callOnRetry(slog.Default(), "retry", p.OnRetry, err, attempt+1, wait)
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}

		attempt++
	}
}

// Backoff computes successive exponential backoff delays. It wraps the
// underlying backoff implementation so callers don't depend on third-party
// types. Use Policy.NewBackoff to create one.
type Backoff struct {
	bo *backoff.ExponentialBackOff
}

// NewBackoff creates a Backoff configured from the policy's parameters.
// Call Next() to get successive delays and Reset() to restart the sequence.
func (p Policy) NewBackoff() *Backoff {
	mustValidatePolicy("retry: Policy.NewBackoff", p)
	return &Backoff{bo: newBackOff(p)}
}

// Next returns the next backoff delay in the sequence.
func (b *Backoff) Next() time.Duration {
	return b.bo.NextBackOff()
}

// Reset restarts the backoff sequence from the initial delay.
func (b *Backoff) Reset() {
	b.bo.Reset()
}

// newBackOff creates an ExponentialBackOff from the policy. We wrap
// cenkalti/backoff rather than implementing exponential backoff from scratch
// because it provides well-tested jitter, clamping, and overflow handling.
// The wrapper adds: stability detection (StableReset), retry predicates
// (RetryIf), structured logging (OnRetry), and context-aware cancellation —
// none of which cenkalti/backoff provides out of the box.
func newBackOff(p Policy) *backoff.ExponentialBackOff {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = p.BaseDelay
	bo.MaxInterval = p.MaxDelay
	bo.Multiplier = p.Factor
	bo.RandomizationFactor = p.Jitter
	return bo
}

func mustValidatePolicy(_ string, p Policy) {
	if err := p.Validate(); err != nil {
		panic("retry: invalid policy")
	}
}

// Validate returns an error when a Policy contains invalid timing or backoff
// settings. Constructors and execution helpers panic on this error so retry
// misconfiguration fails fast rather than creating tight retry loops.
func (p Policy) Validate() error {
	if p.BaseDelay <= 0 {
		return fmt.Errorf("base delay must be > 0")
	}
	if p.MaxDelay <= 0 {
		return fmt.Errorf("max delay must be > 0")
	}
	if p.Factor < 1 {
		return fmt.Errorf("factor must be >= 1")
	}
	if p.Jitter < 0 || p.Jitter > 1 {
		return fmt.Errorf("jitter must be between 0 and 1")
	}
	if p.StableReset < 0 {
		return fmt.Errorf("stable reset must be >= 0")
	}
	if p.MaxElapsedTime < 0 {
		return fmt.Errorf("max elapsed time must be >= 0")
	}
	return nil
}
