package amqpbackend

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/messaging"
	"github.com/bds421/rho-kit/resilience/v2/retry"
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
//
// FR-070 [MED]: panics on n <= 0 — RabbitMQ interprets prefetch 0
// as "unlimited", which removes consumer backpressure entirely. A
// typo there would let one slow consumer hold the entire queue
// in-flight.
func WithPrefetch(n int) ConsumerOption {
	if n <= 0 {
		panic("amqpbackend: WithPrefetch requires n > 0; RabbitMQ treats 0 as unlimited prefetch")
	}
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

// WithConsumerMetrics attaches Prometheus metrics to the consumer.
func WithConsumerMetrics(m *Metrics) ConsumerOption {
	if m == nil {
		panic("amqpbackend: WithConsumerMetrics requires non-nil metrics")
	}
	return func(c *Consumer) { c.metrics = m }
}

// Consumer reads messages from an AMQP queue and dispatches them to a Handler.
type Consumer struct {
	conn                  Connector
	publisher             DeadLetterPublisher
	logger                *slog.Logger
	prefetch              int
	hooks                 ConsumerHooks
	metrics               *Metrics
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
// The value must be positive; use [WithoutMaxDLQConsecutiveFailures] only
// for tests that need to observe uncapped DLQ retry behavior.
func WithMaxDLQConsecutiveFailures(n int) ConsumerOption {
	if n <= 0 {
		panic("amqpbackend: WithMaxDLQConsecutiveFailures requires n > 0")
	}
	return func(c *Consumer) { c.maxDLQConsecutiveFail = n }
}

// WithoutMaxDLQConsecutiveFailures disables the consecutive DLQ failure cap.
func WithoutMaxDLQConsecutiveFailures() ConsumerOption {
	return func(c *Consumer) { c.maxDLQConsecutiveFail = 0 }
}

// NewConsumer creates a Consumer bound to the given connection.
// The publisher is used for confirmed dead-letter publishes when consuming
// bindings with retry. Pass nil if no retry bindings will be consumed.
//
// Panics if conn is nil — the consumer dereferences it on every channel
// open, so accepting nil here would only defer the crash to the first
// delivery. A nil logger is normalised to [slog.Default].
func NewConsumer(conn Connector, publisher DeadLetterPublisher, logger *slog.Logger, opts ...ConsumerOption) *Consumer {
	if conn == nil {
		panic("amqpbackend: NewConsumer requires a non-nil Connector")
	}
	if logger == nil {
		logger = slog.Default()
	}
	c := &Consumer{
		conn:                  conn,
		publisher:             publisher,
		logger:                logger,
		prefetch:              defaultPrefetch,
		maxDLQConsecutiveFail: defaultMaxDLQConsecutiveFailures,
	}
	for _, opt := range opts {
		if opt == nil {
			panic("amqp: Consumer option must not be nil")
		}
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
	if handler == nil {
		panic("amqpbackend: ConsumeOnce requires a non-nil handler")
	}
	if b.Retry != nil && c.publisher == nil {
		return fmt.Errorf("consumeOnce with retry requires a publisher (pass non-nil publisher to NewConsumer)")
	}
	switch {
	case b.Retry == nil && b.WithoutRetry:
		c.logger.Info("consumer binding configured with WithoutRetry — handler errors will ack-and-discard the message",
			redact.String("queue", b.Queue), redact.String("exchange", b.Exchange))
	case b.Retry == nil && !b.WithoutRetry:
		c.logger.Error("consumer binding reached the consumer with no retry policy and WithoutRetry=false — defaults should have been applied via DeclareAll/ComputeBindings; treating as drop-on-error",
			redact.String("queue", b.Queue), redact.String("exchange", b.Exchange))
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

	c.logger.Info("consumer started", redact.String("queue", b.Queue))

	for {
		select {
		case <-ctx.Done():
			c.logger.Info("consumer stopping", redact.String("queue", b.Queue))
			return ctx.Err()

		case delivery, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("delivery channel closed")
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
		c.logger.Error("unmarshal message failed, discarding", redact.Error(err))
		// Ack (not nack) — a malformed message will never parse successfully,
		// so retrying via DLX is pointless and would create an infinite loop.
		if ackErr := delivery.Ack(false); ackErr != nil {
			c.logger.Error("ack failed after unmarshal error", redact.Error(ackErr))
		}
		c.onDiscard("", "", b.Queue)
		c.metrics.observeConsumed(b.Queue, amqpConsumeOutcomeDecodeError)
		return
	}

	d := fromAMQPDelivery(delivery, msg)

	handlerStarted := time.Now()
	handlerErr := c.invokeHandler(ctx, handler, d, msg, b.Queue)
	if handlerErr != nil {
		c.metrics.observeHandler(b.Queue, amqpHandlerOutcomeError, handlerStarted)
	} else {
		c.metrics.observeHandler(b.Queue, amqpHandlerOutcomeSuccess, handlerStarted)
	}
	if handlerErr != nil {
		c.handleFailure(ctx, delivery, msg, b, handlerErr)
		return
	}

	if ackErr := delivery.Ack(false); ackErr != nil {
		c.logger.Error("ack failed", redact.Error(ackErr), redact.String("id", msg.ID))
		c.metrics.observeConsumed(b.Queue, amqpConsumeOutcomeAckFailed)
		return
	}
	c.metrics.observeConsumed(b.Queue, amqpConsumeOutcomeAcked)
}

// invokeHandler runs the user handler with panic recovery. A panic is
// converted to a permanent error so handleFailure routes it through
// dead-letter handling (when configured) without nack-requeue, preventing
// a poison-pill panic from killing the consumer goroutine and leaving the
// delivery unacked until channel teardown. NATS already has equivalent
// recovery in dispatch.
func (c *Consumer) invokeHandler(ctx context.Context, handler messaging.Handler, d messaging.Delivery, msg messaging.Message, queue string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("amqpbackend: handler panicked",
				redact.Panic(r),
				redact.String("id", msg.ID),
				redact.String("type", msg.Type),
				redact.String("queue", queue),
			)
			err = apperror.NewPermanentWithCause("handler panicked", fmt.Errorf("%s", redact.PanicValue(r)))
		}
	}()
	return handler(ctx, d)
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

func (c *Consumer) handleFailure(ctx context.Context, delivery amqp.Delivery, msg messaging.Message, b messaging.Binding, handlerErr error) {
	// Permanent errors (e.g. structurally invalid messages) will never
	// succeed on retry. Audit FR-071 [HIGH]: route them to the dead
	// exchange when configured so a poison message lands in a
	// broker-visible DLQ instead of vanishing. When no dead exchange
	// is configured, fall back to the original ack-discard behaviour
	// so callers that opted out of DLQ infrastructure still get
	// retry-budget protection from poison messages.
	if apperror.IsPermanent(handlerErr) {
		if b.DeadExchange != "" {
			c.logger.Warn("permanent handler error, dead-lettering message",
				redact.Error(handlerErr),
				redact.String("id", msg.ID),
				redact.String("type", msg.Type),
			)
			c.metrics.observeConsumed(b.Queue, c.routeToDeadExchange(ctx, delivery, msg, b, handlerErr, 0))
			return
		}
		c.logger.Warn("permanent handler error, discarding message (no dead exchange configured)",
			redact.Error(handlerErr),
			redact.String("id", msg.ID),
			redact.String("type", msg.Type),
		)
		if ackErr := delivery.Ack(false); ackErr != nil {
			c.logger.Error("ack failed", redact.Error(ackErr))
		}
		c.onDiscard(msg.ID, msg.Type, b.Queue)
		c.metrics.observeConsumed(b.Queue, amqpConsumeOutcomeDiscarded)
		return
	}

	action, retryCount := resolveFailure(delivery, b)

	switch action {
	case actionForceDiscard:
		c.logger.Error("safety limit exceeded, force-discarding message",
			redact.Error(handlerErr),
			redact.String("id", msg.ID),
			redact.String("type", msg.Type),
			"retries", retryCount,
			"safety_limit", b.Retry.MaxRetries*safetyMaxBounceMultiplier,
		)
		// Ack (not nack) — when DLX is configured, nack routes the message
		// back to the retry queue, creating an infinite bounce loop.
		if ackErr := delivery.Ack(false); ackErr != nil {
			c.logger.Error("ack failed", redact.Error(ackErr))
		}
		c.onDiscard(msg.ID, msg.Type, b.Queue)
		c.metrics.observeConsumed(b.Queue, amqpConsumeOutcomeForceDiscarded)

	case actionDeadLetter:
		c.logger.Error("max retries exceeded, moving to dead queue",
			redact.Error(handlerErr),
			redact.String("id", msg.ID),
			redact.String("type", msg.Type),
			"retries", retryCount,
		)
		c.metrics.observeConsumed(b.Queue, c.routeToDeadExchange(ctx, delivery, msg, b, handlerErr, retryCount))

	case actionRetry:
		c.logger.Warn("handler failed, nacking for DLX retry",
			redact.Error(handlerErr),
			redact.String("id", msg.ID),
			redact.String("type", msg.Type),
			"retry", retryCount,
		)
		if nackErr := delivery.Nack(false, false); nackErr != nil {
			c.logger.Error("nack failed", redact.Error(nackErr))
		}
		c.onRetry(msg.ID, msg.Type, b.Queue, retryCount)
		c.metrics.observeConsumed(b.Queue, amqpConsumeOutcomeRetry)

	case actionDiscard:
		c.logger.Warn("handler failed, discarding message (no retry configured)",
			redact.Error(handlerErr),
			redact.String("id", msg.ID),
			redact.String("type", msg.Type),
		)
		// Ack (not nack) — defensively prevent unexpected routing if an operator
		// manually adds a DLX to the queue via RabbitMQ management UI.
		if ackErr := delivery.Ack(false); ackErr != nil {
			c.logger.Error("ack failed", redact.Error(ackErr))
		}
		c.onDiscard(msg.ID, msg.Type, b.Queue)
		c.metrics.observeConsumed(b.Queue, amqpConsumeOutcomeDiscarded)
	}
}

// routeToDeadExchange publishes the original delivery bytes to the
// configured dead exchange and acks on success, with safeguards
// against a misconfigured DLE bouncing messages forever.
//
// On publish failure we'd normally nack-discard so the message
// re-enters the retry cycle, but a PERMANENTLY broken dead exchange
// (typo, missing binding) would bounce each message
// MaxRetries × safetyMaxBounceMultiplier times against the failing
// exchange. After defaultMaxDLQConsecutiveFailures consecutive
// failures we flip to force-discard with a loud log line so operators
// can fix the DLE config rather than thrashing forever. Any
// successful publish resets the counter.
//
// retryCount is reported through OnDeadLetter for observability;
// pass 0 for permanent-error routings (audit FR-071) where the
// message never made it to a retry attempt.
func (c *Consumer) routeToDeadExchange(ctx context.Context, delivery amqp.Delivery, msg messaging.Message, b messaging.Binding, handlerErr error, retryCount int) string {
	// FR-072 [MED]: derive from the caller's ctx so trace values
	// and shutdown deadlines propagate to the DLE publish, while
	// still capping per-publish runtime via deadLetterPublishTimeout.
	parent := ctx
	if parent == nil {
		parent = context.Background()
	}
	deadCtx, deadCancel := context.WithTimeout(parent, deadLetterPublishTimeout)
	pubErr := c.publishRawDeadLetter(deadCtx, b.DeadExchange, b.Queue, delivery.Body, msg.ID)
	deadCancel()
	if pubErr != nil {
		fails := c.dlqConsecutiveFail.Add(1)
		capped := c.maxDLQConsecutiveFail > 0 && int(fails) > c.maxDLQConsecutiveFail
		if capped {
			c.logger.Error("dead-letter publish has failed repeatedly, force-discarding to break the loop — fix the dead exchange",
				redact.Error(pubErr),
				redact.String("id", msg.ID),
				"consecutive_failures", fails,
				"cap", c.maxDLQConsecutiveFail,
				redact.ErrorKey("handler_error", handlerErr))
			if ackErr := delivery.Ack(false); ackErr != nil {
				c.logger.Error("ack failed during DLE-force-discard", redact.Error(ackErr))
			}
			c.onDiscard(msg.ID, msg.Type, b.Queue)
			return amqpConsumeOutcomeForceDiscarded
		}
		c.logger.Error("dead-letter publish failed, nacking to retry",
			redact.Error(pubErr),
			redact.String("id", msg.ID),
			"consecutive_failures", fails,
			redact.ErrorKey("handler_error", handlerErr))
		if nackErr := delivery.Nack(false, false); nackErr != nil {
			c.logger.Error("nack failed after dead-letter publish failure", redact.Error(nackErr))
		}
		return amqpConsumeOutcomeDLQPublishFailed
	}
	c.dlqConsecutiveFail.Store(0)
	if ackErr := delivery.Ack(false); ackErr != nil {
		c.logger.Error("ack failed after dead-letter publish", redact.Error(ackErr), redact.String("id", msg.ID))
	}
	c.onDeadLetter(msg.ID, msg.Type, b.Queue, retryCount)
	return amqpConsumeOutcomeDeadLettered
}

func (c *Consumer) publishRawDeadLetter(ctx context.Context, exchange, routingKey string, body []byte, msgID string) (err error) {
	if c.publisher == nil {
		return fmt.Errorf("dead-letter publisher is not configured")
	}
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("amqpbackend: dead-letter publisher panicked",
				redact.Panic(r),
				redact.String("exchange", exchange),
				redact.String("routing_key", routingKey),
				redact.String("msg_id", msgID),
				"stack", string(debug.Stack()),
			)
			err = fmt.Errorf("dead-letter publisher panic: %s", redact.PanicValue(r))
		}
	}()
	return c.publisher.PublishRaw(ctx, exchange, routingKey, body, msgID)
}

func (c *Consumer) onRetry(msgID, msgType, queue string, retryCount int) {
	if c.hooks.OnRetry == nil {
		return
	}
	c.callHook("OnRetry", func() {
		c.hooks.OnRetry(msgID, msgType, queue, retryCount)
	})
}

func (c *Consumer) onDeadLetter(msgID, msgType, queue string, retryCount int) {
	if c.hooks.OnDeadLetter == nil {
		return
	}
	c.callHook("OnDeadLetter", func() {
		c.hooks.OnDeadLetter(msgID, msgType, queue, retryCount)
	})
}

func (c *Consumer) onDiscard(msgID, msgType, queue string) {
	if c.hooks.OnDiscard == nil {
		return
	}
	c.callHook("OnDiscard", func() {
		c.hooks.OnDiscard(msgID, msgType, queue)
	})
}

func (c *Consumer) callHook(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			c.logger.Error("amqpbackend: consumer hook panicked",
				"hook", name,
				redact.Panic(r),
				"stack", string(debug.Stack()),
			)
		}
	}()
	fn()
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
