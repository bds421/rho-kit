package messaging

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/bds421/rho-kit/core/v2/redact"
)

// Subscription is a self-contained (Consumer, Binding, Handler)
// triple that knows how to run itself as a
// [github.com/bds421/rho-kit/runtime/v2/lifecycle.Component].
//
// Wave 165 introduces this as the kit's mid-level messaging
// abstraction: above the raw [Consumer] interface (which leaves
// lifecycle wiring to the caller) and below [TypedSubscription]
// (which adds payload decode + validate). A service that wires
// several subscriptions can compose them via [SubscriptionGroup] or
// register each as a sibling under [lifecycle.Runner] — the kit no
// longer requires bespoke goroutine plumbing per consumer.
//
// Concurrency: every method is safe for concurrent use. Start may
// be called at most once; a second Start returns an error rather
// than racing the leader flag.
type Subscription struct {
	name     string
	consumer Consumer
	binding  Binding
	handler  Handler
	logger   *slog.Logger

	started atomic.Bool
	cancel  atomic.Pointer[context.CancelFunc]
	done    chan struct{}
}

// SubscriptionOption configures a [Subscription] or
// [TypedSubscription].
type SubscriptionOption func(*subscriptionConfig)

type subscriptionConfig struct {
	logger              *slog.Logger
	skipTypedValidation bool
}

// WithSubscriptionLogger pins the structured logger used by the
// subscription's lifecycle events (start, error, stop). Defaults to
// [slog.Default] when omitted. Nil panics so a misconfigured-but-
// configured caller surfaces at startup.
func WithSubscriptionLogger(logger *slog.Logger) SubscriptionOption {
	if logger == nil {
		panic("messaging: WithSubscriptionLogger requires a non-nil logger (omit the option for slog.Default)")
	}
	return func(c *subscriptionConfig) { c.logger = logger }
}

// NewSubscription constructs a Subscription with the supplied name,
// consumer, binding, and handler. The name is operator-facing and
// is used in log lines and (optionally) Prometheus labels — pick a
// short stable identifier.
//
// Panics on nil consumer / handler or empty name so misconfiguration
// surfaces at startup rather than the first Start invocation.
func NewSubscription(name string, consumer Consumer, binding Binding, handler Handler, opts ...SubscriptionOption) *Subscription {
	if name == "" {
		panic("messaging: NewSubscription requires a non-empty name")
	}
	if consumer == nil {
		panic("messaging: NewSubscription requires a non-nil consumer")
	}
	if handler == nil {
		panic("messaging: NewSubscription requires a non-nil handler")
	}
	cfg := subscriptionConfig{}
	for _, opt := range opts {
		if opt == nil {
			panic("messaging: NewSubscription option must not be nil")
		}
		opt(&cfg)
	}
	if cfg.logger == nil {
		cfg.logger = slog.Default()
	}
	return &Subscription{
		name:     name,
		consumer: consumer,
		binding:  binding,
		handler:  handler,
		logger:   cfg.logger,
		done:     make(chan struct{}),
	}
}

// Name returns the operator-facing identifier supplied to
// [NewSubscription]. Useful for logging and lifecycle.Runner row
// names.
func (s *Subscription) Name() string {
	return s.name
}

// Start runs the consumer loop until ctx is cancelled or the
// consumer surfaces an unrecoverable error. Implements
// [lifecycle.Component].
//
// Start may be called at most once per Subscription. The
// Subscription is NOT re-runnable after Stop — construct a fresh
// one if the workflow needs restart semantics; the
// lifecycle.Runner's restart policy handles process-level
// re-launches.
func (s *Subscription) Start(ctx context.Context) (retErr error) {
	if ctx == nil {
		return errors.New("messaging/subscription: Start requires a non-nil context")
	}
	if !s.started.CompareAndSwap(false, true) {
		return errors.New("messaging/subscription: Start already invoked")
	}
	defer close(s.done)

	runCtx, cancel := context.WithCancel(ctx)
	s.cancel.Store(&cancel)
	defer cancel()

	s.logger.InfoContext(runCtx, "messaging: subscription start",
		redact.String("name", s.name),
	)
	defer func() {
		if retErr != nil {
			s.logger.WarnContext(ctx, "messaging: subscription exit",
				redact.String("name", s.name),
				redact.Error(retErr),
			)
		} else {
			s.logger.InfoContext(ctx, "messaging: subscription exit",
				redact.String("name", s.name),
			)
		}
	}()

	err := s.consumer.Consume(runCtx, s.binding, s.handler)
	if err != nil && !errors.Is(err, context.Canceled) {
		return redact.WrapError("messaging/subscription: consume", err)
	}
	return nil
}

// Stop cancels the consumer loop and waits for Start to return.
// Implements [lifecycle.Component].
//
// Idempotent: a second Stop is a no-op. Safe to call before Start —
// returns immediately because no work has been started.
//
// Concurrency: Start sets the started flag before it publishes the
// cancel func. A Stop that races into that window observes
// started==true but a not-yet-published cancel; rather than cancel
// nothing and then block on done until ctx expires (leaving the
// consumer running on the un-cancelled runCtx), Stop waits for the
// cancel func to appear and then invokes it. The wait is bounded by
// ctx and by Start completing (s.done closes when Start returns,
// covering the case where Start exited before storing a cancel — which
// cannot happen, but keeps Stop from spinning forever on a misbehaving
// caller).
func (s *Subscription) Stop(ctx context.Context) error {
	if ctx == nil {
		return errors.New("messaging/subscription: Stop requires a non-nil context")
	}
	if !s.started.Load() {
		return nil
	}
	if cancel := s.awaitCancel(ctx); cancel != nil {
		(*cancel)()
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// awaitCancel atomically takes the published cancel func, waiting for
// Start to publish it if Stop raced into the window between the started
// CAS and the cancel store. Returns nil when ctx is cancelled or Start
// has already returned (s.done closed) without a cancel to take — both
// terminal states where there is nothing left to cancel.
func (s *Subscription) awaitCancel(ctx context.Context) *context.CancelFunc {
	for {
		if cancel := s.cancel.Swap(nil); cancel != nil {
			return cancel
		}
		select {
		case <-ctx.Done():
			return nil
		case <-s.done:
			// Start returned; cancel (if any) is moot. Take it once more
			// to cover the publish-then-return ordering.
			return s.cancel.Swap(nil)
		default:
			// Yield so concurrent Start can publish cancel without a
			// 100% CPU busy-spin in the Start/Stop race window.
			time.Sleep(time.Millisecond)
		}
	}
}
