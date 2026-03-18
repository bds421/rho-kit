// Package retry provides generic retry and backoff utilities for operations
// that may fail transiently. It consolidates the duplicated backoff patterns
// used across redis, messaging, and notification delivery.
package retry

import (
	"context"
	"log/slog"
	"time"

	"github.com/cenkalti/backoff/v5"

	"github.com/bds421/rho-kit/core/apperror"
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
}

// DefaultPolicy is a sensible default: 3 retries, 1s base, 30s max, 2x factor, ±25% jitter.
var DefaultPolicy = Policy{
	MaxRetries: 3,
	BaseDelay:  1 * time.Second,
	MaxDelay:   30 * time.Second,
	Factor:     2.0,
	Jitter:     0.25,
}

// WorkerPolicy is tuned for long-running worker loops: unlimited retries,
// stability detection, matching the existing redis/messaging patterns.
var WorkerPolicy = Policy{
	MaxRetries:  -1, // unlimited
	BaseDelay:   3 * time.Second,
	MaxDelay:    60 * time.Second,
	Factor:      2.0,
	Jitter:      0.25,
	StableReset: 30 * time.Second,
}

// Option configures a Policy.
type Option func(*Policy)

// WithMaxRetries sets the maximum retry attempts.
func WithMaxRetries(n int) Option { return func(p *Policy) { p.MaxRetries = n } }

// WithBaseDelay sets the initial backoff delay.
func WithBaseDelay(d time.Duration) Option { return func(p *Policy) { p.BaseDelay = d } }

// WithMaxDelay sets the maximum backoff delay.
func WithMaxDelay(d time.Duration) Option { return func(p *Policy) { p.MaxDelay = d } }

// WithFactor sets the exponential backoff multiplier.
func WithFactor(f float64) Option { return func(p *Policy) { p.Factor = f } }

// WithJitter sets the jitter fraction (e.g. 0.25 for ±25%).
func WithJitter(f float64) Option { return func(p *Policy) { p.Jitter = f } }

// WithStableReset enables stability detection: if the function runs for at
// least d before failing, the delay resets to BaseDelay.
func WithStableReset(d time.Duration) Option { return func(p *Policy) { p.StableReset = d } }

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
// The default policy is DefaultPolicy; options override individual fields.
// Returns the last error if all retries are exhausted or ctx is cancelled.
//
// Note: when StableReset and MaxRetries are both set, the attempt counter
// resets after a stable run, effectively allowing more than MaxRetries total
// attempts across stability cycles. Use MaxRetries alone for a firm cap.
func Do(ctx context.Context, fn func(ctx context.Context) error, opts ...Option) error {
	p := DefaultPolicy
	for _, o := range opts {
		o(&p)
	}
	return doWithPolicy(ctx, p, fn)
}

// DoWith executes fn using a specific base policy (overridden by opts).
func DoWith(ctx context.Context, base Policy, fn func(ctx context.Context) error, opts ...Option) error {
	for _, o := range opts {
		o(&base)
	}
	return doWithPolicy(ctx, base, fn)
}

// Loop runs fn in an infinite restart loop with exponential backoff, logging
// errors between restarts. Blocks until ctx is cancelled.
// Uses WorkerPolicy as default; options override individual fields.
func Loop(ctx context.Context, logger *slog.Logger, component string, fn func(ctx context.Context) error, opts ...Option) {
	p := WorkerPolicy
	for _, o := range opts {
		o(&p)
	}

	bo := newBackOff(p)
	attempt := 0
	for {
		// Check ctx before invoking fn to avoid one extra execution
		// after the timer fires but ctx is already cancelled.
		if ctx.Err() != nil {
			return
		}

		start := time.Now()
		err := fn(ctx)

		if ctx.Err() != nil {
			return
		}

		if p.RetryIf != nil && !p.RetryIf(err) {
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

		if p.OnRetry != nil {
			p.OnRetry(err, attempt+1, wait)
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

// Delay computes the backoff delay for a given attempt using the policy.
// Attempt 0 returns BaseDelay, attempt 1 returns BaseDelay*Factor, etc.
// For attempts beyond the backoff sequence, MaxDelay is returned.
// Negative attempts are clamped to 0.
func (p Policy) Delay(attempt int) time.Duration {
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
	bo := newBackOff(p)
	attempt := 0

	for {
		start := time.Now()
		err := fn(ctx)
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if p.RetryIf != nil && !p.RetryIf(err) {
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

		if p.OnRetry != nil {
			p.OnRetry(err, attempt+1, wait)
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
