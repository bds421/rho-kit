package amqpbackend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/bds421/rho-kit/core/v2/redact"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// Publisher sends messages to AMQP exchanges.
// Each publish opens a dedicated channel with confirm mode, publishes,
// waits for the broker acknowledgement, and closes the channel. This
// eliminates head-of-line blocking — concurrent publishes proceed
// independently. AMQP channels are cheap (multiplexed over a single
// TCP connection) and designed for per-goroutine use.
type Publisher struct {
	conn        Connector
	logger      *slog.Logger
	sizeLimiter messaging.MessageSizeLimiter
	metrics     *Metrics
}

// PublisherOption configures a Publisher.
type PublisherOption func(*Publisher)

// WithMessageSizeLimiter replaces the publisher's message-size policy.
func WithMessageSizeLimiter(l messaging.MessageSizeLimiter) PublisherOption {
	return func(p *Publisher) { p.sizeLimiter = l }
}

// WithMaxMessageBytes sets the default serialized message-size limit.
func WithMaxMessageBytes(maxBytes int) PublisherOption {
	return func(p *Publisher) {
		p.sizeLimiter = p.sizeLimiter.WithDefaultMaxBytes(maxBytes)
	}
}

// WithoutMaxMessageBytes disables the default size limit. Route-specific
// limits configured with WithRouteMaxMessageBytes still apply.
func WithoutMaxMessageBytes() PublisherOption {
	return func(p *Publisher) {
		p.sizeLimiter = p.sizeLimiter.WithoutDefaultMaxBytes()
	}
}

// WithRouteMaxMessageBytes overrides the message-size limit for one exact
// exchange+routing-key pair. routingKey may be empty for fanout-style routes.
func WithRouteMaxMessageBytes(exchange, routingKey string, maxBytes int) PublisherOption {
	return func(p *Publisher) {
		p.sizeLimiter = p.sizeLimiter.WithRouteMaxBytes(exchange, routingKey, maxBytes)
	}
}

// WithPublisherMetrics attaches Prometheus metrics to the publisher.
func WithPublisherMetrics(m *Metrics) PublisherOption {
	if m == nil {
		panic("amqpbackend: WithPublisherMetrics requires non-nil metrics")
	}
	return func(p *Publisher) { p.metrics = m }
}

// NewPublisher creates a Publisher bound to the given connection. Panics
// if conn is nil — the publisher dereferences it on every channel open,
// so accepting nil here would only defer the crash to the first publish.
// A nil logger is normalized to [slog.Default].
func NewPublisher(conn Connector, logger *slog.Logger, opts ...PublisherOption) *Publisher {
	if conn == nil {
		panic("amqpbackend: NewPublisher requires a non-nil Connector")
	}
	if logger == nil {
		logger = slog.Default()
	}
	p := &Publisher{
		conn:        conn,
		logger:      logger,
		sizeLimiter: messaging.DefaultMessageSizeLimiter(),
	}
	for _, opt := range opts {
		if opt == nil {
			panic("amqpbackend: Publisher option must not be nil")
		}
		opt(p)
	}
	return p
}

func (p *Publisher) ready() error {
	if p == nil || p.conn == nil || p.logger == nil {
		return messaging.ErrInvalidPublisher
	}
	return nil
}

// Publish serializes and sends a Message to the specified exchange with the given routing key.
// It uses confirm mode to ensure the broker has acknowledged receipt of the message.
func (p *Publisher) Publish(ctx context.Context, exchange, routingKey string, msg messaging.Message) error {
	if err := p.ready(); err != nil {
		return err
	}
	if err := messaging.ValidatePublishContext(ctx); err != nil {
		return err
	}
	if err := messaging.ValidatePublishRoute(exchange, routingKey); err != nil {
		return err
	}
	started := time.Now()
	outcome := amqpPublishOutcomeFailed
	defer func() {
		p.metrics.observePublish(exchange, routingKey, outcome, started)
	}()
	if err := messaging.ValidateMessage(msg); err != nil {
		outcome = amqpPublishOutcomeInvalidMessage
		return err
	}
	if err := p.sizeLimiter.Check(exchange, routingKey, msg); err != nil {
		outcome = publishOutcomeForError(err)
		return err
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	pub := amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    msg.ID,
		Timestamp:    msg.Timestamp,
		Body:         body,
	}
	headerCount := len(msg.Headers)
	if msg.SchemaVersion != 0 {
		headerCount++
	}
	if headerCount > 0 {
		pub.Headers = make(amqp.Table, headerCount)
		for k, v := range msg.Headers {
			pub.Headers[k] = v
		}
		if msg.SchemaVersion != 0 {
			// Clamp to int32 range for AMQP wire format safety.
			sv := msg.SchemaVersion
			if sv > math.MaxInt32 {
				sv = math.MaxInt32
			}
			pub.Headers[messaging.HeaderSchemaVersion] = int32(sv)
		}
	}

	if err := p.publishConfirmed(ctx, exchange, routingKey, pub); err != nil {
		outcome = publishOutcomeForError(err)
		return err
	}
	outcome = amqpPublishOutcomeSuccess

	p.logger.Debug("message published",
		redact.String("id", msg.ID),
		redact.String("type", msg.Type),
		redact.String("exchange", exchange),
		redact.String("routing_key", routingKey),
	)
	return nil
}

// PublishRaw sends pre-serialized bytes to an exchange with confirm mode.
// Use this for dead-letter forwarding where the original body should be
// preserved exactly (no re-serialization round-trip).
func (p *Publisher) PublishRaw(ctx context.Context, exchange, routingKey string, body []byte, msgID string) error {
	if err := p.ready(); err != nil {
		return err
	}
	if err := messaging.ValidatePublishContext(ctx); err != nil {
		return err
	}
	if err := messaging.ValidatePublishRoute(exchange, routingKey); err != nil {
		return err
	}
	started := time.Now()
	outcome := amqpPublishOutcomeFailed
	defer func() {
		p.metrics.observePublish(exchange, routingKey, outcome, started)
	}()
	if maxBytes := p.sizeLimiter.LimitFor(exchange, routingKey); maxBytes > 0 && len(body) > maxBytes {
		outcome = amqpPublishOutcomeTooLarge
		return &messaging.MessageTooLargeError{
			Exchange:   exchange,
			RoutingKey: routingKey,
			Size:       len(body),
			Limit:      maxBytes,
		}
	}
	if err := p.publishConfirmed(ctx, exchange, routingKey, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    msgID,
		Body:         body,
	}); err != nil {
		outcome = publishOutcomeForError(err)
		return err
	}
	outcome = amqpPublishOutcomeSuccess

	p.logger.Debug("raw message published",
		redact.String("id", msgID),
		redact.String("exchange", exchange),
		redact.String("routing_key", routingKey),
	)
	return nil
}

// ErrUnroutable indicates the broker accepted the message but no queue was
// bound to receive it. Callers (outbox/buffered publisher) should treat this
// as a publish failure so the message is retried after topology is fixed,
// rather than silently lost.
var ErrUnroutable = errors.New("amqp: message returned by broker (no route to any queue)")

func publishOutcomeForError(err error) string {
	switch {
	case err == nil:
		return amqpPublishOutcomeSuccess
	case errors.Is(err, messaging.ErrMessageTooLarge):
		return amqpPublishOutcomeTooLarge
	case errors.Is(err, ErrUnroutable):
		return amqpPublishOutcomeUnroutable
	default:
		return amqpPublishOutcomeFailed
	}
}

// publishConfirmed opens a dedicated channel, publishes, waits for the
// broker confirmation, and closes the channel. Each call is independent —
// no shared state, no mutex, no head-of-line blocking.
//
// Mandatory mode is enabled: messages that cannot be routed to any queue
// trigger a basic.return from the broker. The amqp091-go library guarantees
// the return is delivered on NotifyReturn before the corresponding ack, so
// after WaitContext returns we can non-blockingly check for an unroutable
// notification and surface it as ErrUnroutable.
func (p *Publisher) publishConfirmed(ctx context.Context, exchange, routingKey string, pub amqp.Publishing) error {
	ch, err := p.conn.Channel()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}
	defer func() {
		if closeErr := ch.Close(); closeErr != nil {
			p.logger.Debug("failed to close publisher channel", redact.Error(closeErr))
		}
	}()

	// Buffered(1) so the broker's basic.return frame doesn't block the
	// channel reader if we're slow to read it.
	returnCh := make(chan amqp.Return, 1)
	ch.NotifyReturn(returnCh)

	if err := ch.Confirm(false); err != nil {
		return fmt.Errorf("enable confirm mode: %w", err)
	}

	dc, err := ch.PublishWithDeferredConfirmWithContext(ctx,
		exchange, routingKey, true /* mandatory */, false, pub,
	)
	if err != nil {
		return fmt.Errorf("publish message: %w", err)
	}

	acked, err := dc.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("wait for confirm: %w", err)
	}
	if !acked {
		return fmt.Errorf("message was nacked by broker")
	}

	// Non-blocking check: if the broker returned the message as unroutable,
	// the amqp091-go reader has already delivered it to returnCh by the time
	// the ack arrives.
	select {
	case <-returnCh:
		return fmt.Errorf("%w: broker returned message as unroutable", ErrUnroutable)
	default:
		return nil
	}
}

// Close is a no-op — channels are opened and closed per-publish.
// Retained for interface compatibility.
func (p *Publisher) Close() {}
