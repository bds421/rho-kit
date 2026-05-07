package amqpbackend

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/bds421/rho-kit/core/apperror"
	"github.com/bds421/rho-kit/infra/messaging"
	"github.com/bds421/rho-kit/resilience/retry"
)

// DeadLetterPublisher sends pre-serialized message bytes to a dead-letter
// exchange. The Consumer uses this narrow interface — it only needs PublishRaw
// for forwarding messages that exceeded their retry limit.
type DeadLetterPublisher interface {
	PublishRaw(ctx context.Context, exchange, routingKey string, body []byte, msgID string) error
}

const (
	defaultPrefetch          = 10
	handlerShutdownTimeout   = 30 * time.Second // max time for in-flight handler on ctx cancellation
	deadLetterPublishTimeout = 10 * time.Second // max time for dead-letter exchange publish
)

// shutdownSignalKey is the context-value key that the consumer sets when
// invoking a handler during graceful shutdown. Handlers can check the value
// via [IsShutdown] to know that the parent context is winding down even
// though the derived handlerCtx is no longer cancelled (we detach with
// context.WithoutCancel so the handler keeps its grace deadline).
type shutdownSignalKey struct{}

// IsShutdown reports whether ctx carries the consumer's shutdown signal.
// True means "the consumer is in graceful shutdown; finish or fail quickly
// rather than starting any long-lived work". The handler's ctx is still
// scoped to a deadline (handlerShutdownTimeout); IsShutdown is the
// finer-grained signal that the consumer itself is winding down.
func IsShutdown(ctx context.Context) bool {
	_, ok := ctx.Value(shutdownSignalKey{}).(struct{})
	return ok
}

// ConsumerOption configures a Consumer during construction.
type ConsumerOption func(*Consumer)

// WithPrefetch sets the AMQP QoS prefetch count (default 10).
// Lower values are appropriate for slow handlers; higher values
// improve throughput for fast handlers.
func WithPrefetch(n int) ConsumerOption {
	return func(c *Consumer) { c.prefetch = n }
}

// ConsumerHooks provides optional callbacks for observability.
// Each hook is called synchronously after the corresponding failure action.
// Keep hooks fast — slow hooks delay message processing.
type ConsumerHooks struct {
	// OnRetry is called when a message is nacked for DLX retry.
	OnRetry func(msgID, msgType, queue string, retryCount int)
	// OnDeadLetter is called when a message exceeds max retries
	// and is published to the dead-letter exchange.
	OnDeadLetter func(msgID, msgType, queue string, retryCount int)
	// OnDiscard is called when a message is discarded (no retry configured
	// or safety limit exceeded).
	OnDiscard func(msgID, msgType, queue string)
}

// WithHooks registers observability callbacks on the consumer.
func WithHooks(h ConsumerHooks) ConsumerOption {
	return func(c *Consumer) { c.hooks = h }
}

// Consumer reads messages from an AMQP queue and dispatches them to a Handler.
type Consumer struct {
	conn                  Connector
	publisher             DeadLetterPublisher
	logger                *slog.Logger
	prefetch              int
	hooks                 ConsumerHooks
	maxDLQConsecutiveFail int
	dlqConsecutiveFail    atomic.Uint64
}

// defaultMaxDLQConsecutiveFailures bounds how many consecutive dead-letter
// publishes may fail before the consumer flips to force-discard mode. A
// permanently broken dead exchange (typo, missing binding) would otherwise
// bounce each message MaxRetries × safetyMaxBounceMultiplier times against
// the failing exchange — minutes of CPU/network thrash per stuck message.
const defaultMaxDLQConsecutiveFailures = 10

// WithMaxDLQConsecutiveFailures overrides [defaultMaxDLQConsecutiveFailures].
// Pass <= 0 to disable the cap (unsafe; only for tests).
func WithMaxDLQConsecutiveFailures(n int) ConsumerOption {
	return func(c *Consumer) { c.maxDLQConsecutiveFail = n }
}

// NewConsumer creates a Consumer bound to the given connection.
// The publisher is used for confirmed dead-letter publishes when consuming
// bindings with retry. Pass nil if no retry bindings will be consumed.
func NewConsumer(conn Connector, publisher DeadLetterPublisher, logger *slog.Logger, opts ...ConsumerOption) *Consumer {
	c := &Consumer{
		conn:                  conn,
		publisher:             publisher,
		logger:                logger,
		prefetch:              defaultPrefetch,
		maxDLQConsecutiveFail: defaultMaxDLQConsecutiveFailures,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// failureAction classifies what the consumer should do when a handler fails.
type failureAction int

const (
	actionDiscard      failureAction = iota // no retry configured — nack and discard
	actionRetry                             // under max retries — nack for DLX bounce
	actionDeadLetter                        // max retries exceeded — publish to dead exchange + ack
	actionForceDiscard                      // safety net — total bounces exceed safe limit, discard
)

// safetyMaxBounceMultiplier is the multiplier applied to MaxRetries to get the
// absolute ceiling for total DLX bounces. Beyond this, messages are forcibly
// discarded to prevent infinite retry loops (e.g., when dead-letter publish
// keeps failing and the message re-enters the retry cycle).
const safetyMaxBounceMultiplier = 3

// resolveFailure determines the appropriate action for a failed delivery.
func resolveFailure(delivery amqp.Delivery, b messaging.Binding) (failureAction, int) {
	if b.Retry == nil {
		return actionDiscard, 0
	}
	retryCount := xDeathCount(delivery.Headers, b.Queue)

	safetyLimit := b.Retry.MaxRetries * safetyMaxBounceMultiplier
	if retryCount >= safetyLimit {
		return actionForceDiscard, retryCount
	}

	if retryCount >= b.Retry.MaxRetries {
		return actionDeadLetter, retryCount
	}
	return actionRetry, retryCount
}

// ConsumeOnce reads from the binding's queue until the context is cancelled
// or the delivery channel closes (e.g., connection drop). DLX retry behavior
// is derived from the binding's RetryPolicy.
//
// Requires a non-nil publisher on the Consumer when Retry is set.
//
// Retry semantics:
//   - b.Retry != nil: retries via the declared retry topology, then dead-letters.
//   - b.Retry == nil && b.WithoutRetry == true: explicit fire-and-forget;
//     handler errors ack-and-discard the message. Logged at INFO at consumer
//     start so the choice is visible in the startup log.
//   - b.Retry == nil && b.WithoutRetry == false: should not happen — the
//     kit's [messaging.NormalizeBindingSpecs] applies [messaging.DefaultRetryPolicy]
//     and emits a warning. If it slips through (caller built a Binding manually),
//     the consumer logs an ERROR and treats the binding as drop-on-error to
//     avoid breaking startup.
func (c *Consumer) ConsumeOnce(ctx context.Context, b messaging.Binding, handler messaging.Handler) error {
	if b.Retry != nil && c.publisher == nil {
		return fmt.Errorf("consumeOnce with retry requires a publisher (pass non-nil publisher to NewConsumer)")
	}
	switch {
	case b.Retry == nil && b.WithoutRetry:
		c.logger.Info("consumer binding configured with WithoutRetry — handler errors will ack-and-discard the message",
			"queue", b.Queue, "exchange", b.Exchange)
	case b.Retry == nil && !b.WithoutRetry:
		c.logger.Error("consumer binding reached the consumer with no retry policy and WithoutRetry=false — defaults should have been applied via DeclareAll/ComputeBindings; treating as drop-on-error",
			"queue", b.Queue, "exchange", b.Exchange)
	}

	ch, err := c.conn.Channel()
	if err != nil {
		return fmt.Errorf("get channel: %w", err)
	}
	defer func() { _ = ch.Close() }()

	if err := ch.Qos(c.prefetch, 0, false); err != nil {
		return fmt.Errorf("set qos: %w", err)
	}

	deliveries, err := ch.Consume(
		b.Queue,
		"",    // consumer tag (auto-generated)
		false, // auto-ack
		false, // exclusive
		false, // no-local
		false, // no-wait
		nil,
	)
	if err != nil {
		return fmt.Errorf("start consuming: %w", err)
	}

	c.logger.Info("consumer started", "queue", b.Queue)

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("consumer stopping", "queue", b.Queue)
			return ctx.Err()

		case delivery, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("delivery channel closed for queue %s", b.Queue)
			}
			// Always give the handler a bounded context. During normal
			// operation the parent ctx is still live and provides natural
			// cancellation. During shutdown the parent ctx is already
			// cancelled, so we use a detached context with a grace period.
			//
			// Shutdown semantics: when ctx is cancelled, the handler receives
			// a context with a cancelled parent but a fresh deadline
			// (handlerShutdownTimeout). Handlers should check ctx.Err() if
			// they need to know whether the service is shutting down.
			//
			// Always apply a timeout to handler execution, both during
			// normal operation and shutdown. This prevents a stuck handler
			// from permanently stalling the consumer goroutine.
			//
			// During shutdown (parent ctx already cancelled) we use
			// context.WithoutCancel as the base so the handler ctx still
			// has its own deadline (the shutdown grace period) but
			// inherits values from the parent (request IDs, tracing).
			// The previous code used context.Background() which dropped
			// every value carried by the consumer ctx. Handlers that need
			// to detect shutdown must call IsShutdown(ctx) — a true return
			// means "the consumer is winding down; finish quickly".
			base := ctx
			isShutdown := ctx.Err() != nil
			if isShutdown {
				base = context.WithoutCancel(ctx)
			}
			if isShutdown {
				base = context.WithValue(base, shutdownSignalKey{}, struct{}{})
			}
			handlerCtx, handlerCancel := context.WithTimeout(base, handlerShutdownTimeout)
			c.handleDelivery(handlerCtx, delivery, handler, b)
			handlerCancel()
		}
	}
}

func (c *Consumer) handleDelivery(ctx context.Context, delivery amqp.Delivery, handler messaging.Handler, b messaging.Binding) {
	msg, err := unmarshal(delivery)
	if err != nil {
		c.logger.Error("unmarshal message failed, discarding", "error", err)
		// Ack (not nack) — a malformed message will never parse successfully,
		// so retrying via DLX is pointless and would create an infinite loop.
		if ackErr := delivery.Ack(false); ackErr != nil {
			c.logger.Error("ack failed after unmarshal error", "error", ackErr)
		}
		if c.hooks.OnDiscard != nil {
			c.hooks.OnDiscard("", "", b.Queue)
		}
		return
	}

	d := fromAMQPDelivery(delivery, msg)

	if err := handler(ctx, d); err != nil {
		c.handleFailure(delivery, msg, b, err)
		return
	}

	if ackErr := delivery.Ack(false); ackErr != nil {
		c.logger.Error("ack failed", "error", ackErr, "id", msg.ID)
	}
}

// unmarshal is a pure parse function — no I/O, no ACK, no side-effects.
// The caller is responsible for deciding what to do with the delivery on error.
func unmarshal(delivery amqp.Delivery) (messaging.Message, error) {
	var msg messaging.Message
	if err := json.Unmarshal(delivery.Body, &msg); err != nil {
		return messaging.Message{}, fmt.Errorf("unmarshal delivery: %w", err)
	}
	return msg, nil
}

func (c *Consumer) handleFailure(delivery amqp.Delivery, msg messaging.Message, b messaging.Binding, handlerErr error) {
	// Permanent errors (e.g. structurally invalid messages) will never succeed
	// on retry. Ack immediately to avoid wasting retry budget.
	if apperror.IsPermanent(handlerErr) {
		c.logger.Warn("permanent handler error, discarding message",
			"error", handlerErr,
			"id", msg.ID,
			"type", msg.Type,
		)
		if ackErr := delivery.Ack(false); ackErr != nil {
			c.logger.Error("ack failed", "error", ackErr)
		}
		if c.hooks.OnDiscard != nil {
			c.hooks.OnDiscard(msg.ID, msg.Type, b.Queue)
		}
		return
	}

	action, retryCount := resolveFailure(delivery, b)

	switch action {
	case actionForceDiscard:
		c.logger.Error("safety limit exceeded, force-discarding message",
			"error", handlerErr,
			"id", msg.ID,
			"type", msg.Type,
			"retries", retryCount,
			"safety_limit", b.Retry.MaxRetries*safetyMaxBounceMultiplier,
		)
		// Ack (not nack) — when DLX is configured, nack routes the message
		// back to the retry queue, creating an infinite bounce loop.
		if ackErr := delivery.Ack(false); ackErr != nil {
			c.logger.Error("ack failed", "error", ackErr)
		}
		if c.hooks.OnDiscard != nil {
			c.hooks.OnDiscard(msg.ID, msg.Type, b.Queue)
		}

	case actionDeadLetter:
		c.logger.Error("max retries exceeded, moving to dead queue",
			"error", handlerErr,
			"id", msg.ID,
			"type", msg.Type,
			"retries", retryCount,
		)

		// Publish original bytes to dead exchange BEFORE acking. Uses PublishRaw
		// to avoid re-serialization round-trip — the body is exactly what the
		// producer sent.
		//
		// On publish failure we'd normally nack-discard so the message re-enters
		// the retry cycle, but a PERMANENTLY broken dead exchange (typo, missing
		// binding) would bounce each message MaxRetries × safetyMaxBounceMultiplier
		// times against the failing exchange. After defaultMaxDLQConsecutiveFailures
		// consecutive failures we flip to force-discard with a loud log line so
		// operators can fix the DLE config rather than thrashing forever. Any
		// successful publish resets the counter.
		deadCtx, deadCancel := context.WithTimeout(context.Background(), deadLetterPublishTimeout)
		pubErr := c.publisher.PublishRaw(deadCtx, b.DeadExchange, b.Queue, delivery.Body, msg.ID)
		deadCancel()
		if pubErr != nil {
			fails := c.dlqConsecutiveFail.Add(1)
			capped := c.maxDLQConsecutiveFail > 0 && int(fails) > c.maxDLQConsecutiveFail
			if capped {
				c.logger.Error("dead-letter publish has failed repeatedly, force-discarding to break the loop — fix the dead exchange",
					"error", pubErr, "id", msg.ID, "consecutive_failures", fails,
					"cap", c.maxDLQConsecutiveFail)
				if ackErr := delivery.Ack(false); ackErr != nil {
					c.logger.Error("ack failed during DLE-force-discard", "error", ackErr)
				}
				if c.hooks.OnDiscard != nil {
					c.hooks.OnDiscard(msg.ID, msg.Type, b.Queue)
				}
				return
			}
			c.logger.Error("dead-letter publish failed, nacking to retry",
				"error", pubErr, "id", msg.ID, "consecutive_failures", fails)
			if nackErr := delivery.Nack(false, false); nackErr != nil {
				c.logger.Error("nack failed after dead-letter publish failure", "error", nackErr)
			}
			return
		}
		c.dlqConsecutiveFail.Store(0)

		if ackErr := delivery.Ack(false); ackErr != nil {
			c.logger.Error("ack failed after dead-letter publish", "error", ackErr, "id", msg.ID)
		}
		if c.hooks.OnDeadLetter != nil {
			c.hooks.OnDeadLetter(msg.ID, msg.Type, b.Queue, retryCount)
		}

	case actionRetry:
		c.logger.Warn("handler failed, nacking for DLX retry",
			"error", handlerErr,
			"id", msg.ID,
			"type", msg.Type,
			"retry", retryCount,
		)
		if nackErr := delivery.Nack(false, false); nackErr != nil {
			c.logger.Error("nack failed", "error", nackErr)
		}
		if c.hooks.OnRetry != nil {
			c.hooks.OnRetry(msg.ID, msg.Type, b.Queue, retryCount)
		}

	case actionDiscard:
		c.logger.Warn("handler failed, discarding message (no retry configured)",
			"error", handlerErr,
			"id", msg.ID,
			"type", msg.Type,
		)
		// Ack (not nack) — defensively prevent unexpected routing if an operator
		// manually adds a DLX to the queue via RabbitMQ management UI.
		if ackErr := delivery.Ack(false); ackErr != nil {
			c.logger.Error("ack failed", "error", ackErr)
		}
		if c.hooks.OnDiscard != nil {
			c.hooks.OnDiscard(msg.ID, msg.Type, b.Queue)
		}
	}
}

// Consume wraps ConsumeOnce in a resilient restart loop. When the AMQP
// connection drops, the delivery channel closes and ConsumeOnce returns.
// The loop waits with exponential backoff, then retries. If OnReconnect was
// configured on the Connection, topology is re-declared automatically
// before consumers retry. The backoff resets after a stable session
// (running longer than 30s). Blocks until ctx is cancelled.
func (c *Consumer) Consume(ctx context.Context, b messaging.Binding, handler messaging.Handler) error {
	retry.Loop(ctx, c.logger, "consumer["+b.Queue+"]", func(ctx context.Context) error {
		return c.ConsumeOnce(ctx, b, handler)
	})
	return ctx.Err()
}
