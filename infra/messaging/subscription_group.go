package messaging

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// SubscriptionGroup runs several [Subscription] values as a single
// [github.com/bds421/rho-kit/runtime/v2/lifecycle.Component]. Useful
// when a service owns many subscriptions and the operator wants
// them to share a single start/stop boundary rather than registering
// each one separately with the [lifecycle.Runner].
//
// Failure isolation: when one subscription's Start returns a
// non-nil error, the group cancels the others and Start returns
// the first error (with the others joined into the chain via
// [errors.Join]). The contract assumes subscriptions are
// independent; if a service has fan-out dependencies between
// subscriptions, wire them separately and let the runner restart
// the dependents.
//
// Concurrency: safe for concurrent reads after Start. Add MUST be
// called before Start — adding after Start returns an error
// rather than racing the group's internal state.
type SubscriptionGroup struct {
	logger *slog.Logger

	mu      sync.Mutex
	subs    []*Subscription
	started atomic.Bool

	cancel atomic.Pointer[context.CancelFunc]
	wg     sync.WaitGroup
	// done is closed when Start returns. Stop waits on it rather than
	// running its own wg.Wait(), which would otherwise race Start's
	// wg.Add and violate sync.WaitGroup reuse rules.
	done chan struct{}
}

// NewSubscriptionGroup constructs an empty group. Use [Add] to
// register subscriptions before [Start].
//
// logger defaults to [slog.Default] when nil — no panic, since the
// group is a building block and the user-facing surface is the
// lifecycle.Runner that owns it.
func NewSubscriptionGroup(logger *slog.Logger) *SubscriptionGroup {
	if logger == nil {
		logger = slog.Default()
	}
	return &SubscriptionGroup{logger: logger, done: make(chan struct{})}
}

// Add registers a subscription. Panics if sub is nil so a
// misconfigured wiring fails fast. Returns an error if Start has
// already been invoked — the group is single-cycle, like
// [lifecycle.FuncComponent].
func (g *SubscriptionGroup) Add(sub *Subscription) error {
	if sub == nil {
		panic("messaging: SubscriptionGroup.Add requires a non-nil Subscription")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.started.Load() {
		return errors.New("messaging/subscription-group: Add after Start")
	}
	g.subs = append(g.subs, sub)
	return nil
}

// Len reports the number of registered subscriptions. Useful for
// metrics and for tests that want to assert wiring before Start.
func (g *SubscriptionGroup) Len() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.subs)
}

// Start blocks while all subscriptions run. Returns when ctx is
// cancelled or any subscription's Start returns an error. The
// first non-nil error is preserved as the group's exit reason;
// subsequent errors (from siblings cancelled by the first one) are
// joined into the chain so callers see every termination signal.
func (g *SubscriptionGroup) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("messaging/subscription-group: Start requires a non-nil context")
	}
	if !g.started.CompareAndSwap(false, true) {
		return errors.New("messaging/subscription-group: Start already invoked")
	}
	// Signal Stop that Start has returned. Stop waits on this instead of
	// running its own wg.Wait(), avoiding a WaitGroup-reuse race against
	// the wg.Add below.
	defer close(g.done)

	// Publish the cancel func before the empty-group early return so a
	// Stop that races Start can always cancel the group — even when the
	// group is empty and Start is parked on the parent context.
	runCtx, cancel := context.WithCancel(ctx)
	g.cancel.Store(&cancel)
	defer cancel()

	g.mu.Lock()
	subs := append([]*Subscription(nil), g.subs...)
	g.mu.Unlock()
	if len(subs) == 0 {
		// Empty group is a no-op rather than an error: composing a
		// group conditionally (only some subscriptions present in
		// some deployments) is idiomatic and should not require
		// callers to special-case the empty case.
		<-runCtx.Done()
		return ctx.Err()
	}

	errCh := make(chan error, len(subs))
	g.wg.Add(len(subs))
	for _, sub := range subs {
		sub := sub
		go func() {
			defer g.wg.Done()
			if err := sub.Start(runCtx); err != nil {
				errCh <- err
				// Cancel siblings so the group exits promptly when
				// one consumer fails.
				cancel()
			}
		}()
	}

	// Wait for either ctx cancellation or every goroutine to finish.
	g.wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		g.logger.WarnContext(ctx, "messaging: subscription group exit with error(s)",
			redact.ErrorChain(errors.Join(errs...)),
		)
		return errors.Join(errs...)
	}
	return ctx.Err()
}

// Stop cancels the group's internal context and waits for every
// subscription to drain. Idempotent and safe for concurrent use.
//
// Safe to call before Start — returns immediately because no work has
// been started. A Stop that races Start waits for Start to publish the
// group's cancel func (the window between the started CAS and the cancel
// store) and then invokes it, rather than cancelling nothing and parking
// until ctx expires while the consumers keep running.
func (g *SubscriptionGroup) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("messaging/subscription-group: Stop requires a non-nil context")
	}
	if !g.started.Load() {
		return nil
	}
	if cancel := g.awaitCancel(ctx); cancel != nil {
		(*cancel)()
	}
	// Wait on the Start goroutine instead of running our own wg.Wait():
	// a concurrent wg.Wait racing Start's wg.Add violates WaitGroup reuse
	// rules. done closes when Start returns (after its own wg.Wait).
	select {
	case <-g.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// awaitCancel atomically takes the published cancel func, waiting for
// Start to publish it if Stop raced into the window between the started
// CAS and the cancel store. Returns nil when ctx is cancelled or Start
// has already returned (g.done closed) without a cancel left to take.
// Cancelling is idempotent, so replacing the previous sync.Once with
// this take-and-call pattern keeps Stop one-shot in effect without a
// once that an early (pre-Start) call could burn.
func (g *SubscriptionGroup) awaitCancel(ctx context.Context) *context.CancelFunc {
	for {
		if cancel := g.cancel.Swap(nil); cancel != nil {
			return cancel
		}
		select {
		case <-ctx.Done():
			return nil
		case <-g.done:
			return g.cancel.Swap(nil)
		default:
			// Yield so concurrent Start can publish cancel without a
			// 100% CPU busy-spin in the Start/Stop race window.
			time.Sleep(time.Millisecond)
		}
	}
}
