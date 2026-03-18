package amqpbackend

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
	conn      Connector
	publisher DeadLetterPublisher
	logger    *slog.Logger
	prefetch  int
	hooks     ConsumerHooks
}

// NewConsumer creates a Consumer bound to the given connection.
// The publisher is used for confirmed dead-letter publishes when consuming
// bindings with retry. Pass nil if no retry bindings will be consumed.
func NewConsumer(conn Connector, publisher DeadLetterPublisher, logger *slog.Logger, opts ...ConsumerOption) *Consumer {
	c := &Consumer{conn: conn, publisher: publisher, logger: logger, prefetch: defaultPrefetch}
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
func (c *Consumer) ConsumeOnce(ctx context.Context, b messaging.Binding, handler messaging.Handler) error {
	if b.Retry != nil && c.publisher == nil {
		return fmt.Errorf("consumeOnce with retry requires a publisher (pass non-nil publisher to NewConsumer)")
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
			// We always apply the grace timeout to prevent the racy
			// select-both-ready case where ctx cancels between the select
			// pick and the ctx.Err() check.
			// Always apply a timeout to handler execution, both during normal
			// operation and shutdown. This prevents a stuck handler from
			// permanently stalling the consumer goroutine.
			var handlerCtx context.Context
			var handlerCancel context.CancelFunc
			if ctx.Err() != nil {
				handlerCtx, handlerCancel = context.WithTimeout(context.Background(), handlerShutdownTimeout)
			} else {
				handlerCtx, handlerCancel = context.WithTimeout(ctx, handlerShutdownTimeout)
			}
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
		// producer sent. If publish fails, nack instead — message re-enters
		// retry cycle, which is better than silent loss.
		deadCtx, deadCancel := context.WithTimeout(context.Background(), deadLetterPublishTimeout)
		pubErr := c.publisher.PublishRaw(deadCtx, b.DeadExchange, b.Queue, delivery.Body, msg.ID)
		deadCancel()
		if pubErr != nil {
			c.logger.Error("dead-letter publish failed, nacking to retry",
				"error", pubErr, "id", msg.ID)
			if nackErr := delivery.Nack(false, false); nackErr != nil {
				c.logger.Error("nack failed after dead-letter publish failure", "error", nackErr)
			}
			return
		}

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
