// Package app wires the background-worker EXAMPLE.
//
// Composition shown:
//
//	messaging.TypedSubscription[OrderEvent]
//	  → resilience/circuitbreaker  (downstream protection)
//	    → resilience/retry          (transient-failure retry)
//	      → typed business handler
//
// The wiring order is the canonical kit pattern:
//   - The OUTER wrapper is the circuit breaker, so when the
//     downstream is broken the breaker rejects fast WITHOUT
//     burning retry attempts.
//   - The INNER wrapper is the retry policy, so transient
//     blips inside a half-open breaker still get a couple of
//     attempts before being counted as a failure.
//
// SECURITY: this example uses an in-process fake Consumer so the
// smoke test stands up without an external broker. Production
// wiring picks a real backend Consumer
// (amqpbackend/kafkabackend/natsbackend/redisbackend). The
// Subscription wiring is unchanged across backends — that's the
// whole point of the messaging.Consumer interface and
// TypedSubscription[T] in wave 165.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bds421/rho-kit/infra/v2/messaging"
	"github.com/bds421/rho-kit/resilience/v2/circuitbreaker"
	"github.com/bds421/rho-kit/resilience/v2/retry"
)

// OrderEvent is the typed payload the worker consumes. In a real
// service the schema would live in a shared package consumed by
// publishers and subscribers; here we inline it for clarity.
type OrderEvent struct {
	OrderID string  `json:"order_id" validate:"required"`
	Amount  float64 `json:"amount"   validate:"gte=0"`
}

// Run starts the worker subscription and blocks until ctx is
// cancelled.
func Run(ctx context.Context) error {
	logger := slog.Default()

	// In a production wiring, replace fakeConsumer with one of the
	// kit's backend Consumers. The Subscription wiring is unchanged.
	consumer := newFakeConsumer()
	binding := messaging.Binding{
		BindingSpec: messaging.BindingSpec{
			Exchange:      "orders",
			ConsumerGroup: "billing",
			RoutingKey:    "order.created",
			WithoutRetry:  true, // we handle retry in the handler chain
		},
	}

	processor := newOrderProcessor(logger)
	resilient := wrapWithResilience(processor.handle, logger)
	typed := func(ctx context.Context, ev OrderEvent, _ messaging.Delivery) error {
		return resilient(ctx, ev)
	}

	sub := messaging.NewTypedSubscription[OrderEvent](
		"orders-billing",
		consumer,
		binding,
		typed,
		messaging.WithSubscriptionLogger(logger),
	)
	logger.Info("background-worker running; press Ctrl-C to stop")
	if err := sub.Start(ctx); err != nil {
		return fmt.Errorf("start subscription: %w", err)
	}
	return nil
}

// wrapWithResilience composes the canonical breaker(retry(handler))
// chain. The breaker is the outer wrapper so when it's open we
// reject fast without burning retry attempts; the retry is the
// inner wrapper so transient blips inside a half-open breaker
// still get a couple of attempts.
func wrapWithResilience(
	inner func(ctx context.Context, ev OrderEvent) error,
	logger *slog.Logger,
) func(ctx context.Context, ev OrderEvent) error {
	breaker := circuitbreaker.NewCircuitBreaker(
		5 /* trip after 5 consecutive failures */,
		10*time.Second, /* cooldown */
		circuitbreaker.WithName("downstream-billing"),
	)
	policy := retry.Policy{
		MaxRetries: 2,
		BaseDelay:  100 * time.Millisecond,
		MaxDelay:   1 * time.Second,
		Factor:     2.0,
		Jitter:     0.25,
		OnRetry: func(err error, attempt int, delay time.Duration) {
			logger.Warn("retrying handler", "attempt", attempt, "delay", delay, "error", err)
		},
	}
	return func(ctx context.Context, ev OrderEvent) error {
		return breaker.ExecuteCtx(ctx, func(ctx context.Context) error {
			return retry.DoWith(ctx, policy, func(ctx context.Context) error {
				return inner(ctx, ev)
			})
		})
	}
}

// orderProcessor is the example's domain logic. In a real service
// this would write to a database, call a downstream API, or
// publish a follow-up event.
type orderProcessor struct {
	logger    *slog.Logger
	mu        sync.Mutex
	processed []OrderEvent
}

func newOrderProcessor(logger *slog.Logger) *orderProcessor {
	return &orderProcessor{logger: logger}
}

func (p *orderProcessor) handle(_ context.Context, ev OrderEvent) error {
	p.mu.Lock()
	p.processed = append(p.processed, ev)
	p.mu.Unlock()
	p.logger.Info("processed order", "order_id", ev.OrderID, "amount", ev.Amount)
	return nil
}

func (p *orderProcessor) snapshot() []OrderEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]OrderEvent, len(p.processed))
	copy(out, p.processed)
	return out
}

// fakeConsumer is a minimal in-process Consumer implementation used
// in the example for testability. It accepts deliveries pushed via
// Inject and dispatches them to the handler installed by
// ConsumeOnce / Consume. In a real service this is replaced with
// the kit's amqp/kafka/nats/redis backend Consumer.
type fakeConsumer struct {
	mu      sync.Mutex
	handler messaging.Handler
	binding messaging.Binding
	inbox   chan messaging.Delivery
	running atomic.Bool
}

func newFakeConsumer() *fakeConsumer {
	return &fakeConsumer{
		inbox: make(chan messaging.Delivery, 64),
	}
}

func (c *fakeConsumer) Consume(ctx context.Context, b messaging.Binding, h messaging.Handler) error {
	c.mu.Lock()
	c.handler = h
	c.binding = b
	c.mu.Unlock()
	c.running.Store(true)
	defer c.running.Store(false)
	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-c.inbox:
			if !ok {
				return nil
			}
			if err := h(ctx, d); err != nil {
				// Best-effort: log via stderr in the example. A real
				// backend would handle ack/nack here.
				slog.Default().Warn("handler returned error", "error", err)
			}
		}
	}
}

func (c *fakeConsumer) ConsumeOnce(ctx context.Context, b messaging.Binding, h messaging.Handler) error {
	c.mu.Lock()
	c.handler = h
	c.binding = b
	c.mu.Unlock()
	select {
	case <-ctx.Done():
		return nil
	case d, ok := <-c.inbox:
		if !ok {
			return nil
		}
		return h(ctx, d)
	}
}

// Inject pushes a payload into the fake consumer's inbox. Used by
// the smoke test to drive the worker.
func (c *fakeConsumer) Inject(ev OrderEvent) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	d := messaging.Delivery{
		Message: messaging.Message{
			ID:        ev.OrderID,
			Type:      "order.created",
			Payload:   payload,
			Timestamp: time.Now().UTC(),
		},
		Exchange:   "orders",
		RoutingKey: "order.created",
	}
	select {
	case c.inbox <- d:
		return nil
	case <-time.After(100 * time.Millisecond):
		return errors.New("inbox full")
	}
}
