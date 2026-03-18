package amqpbackend

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/bds421/rho-kit/infra/messaging"
)

// Publisher sends messages to AMQP exchanges.
// Each publish opens a dedicated channel with confirm mode, publishes,
// waits for the broker acknowledgement, and closes the channel. This
// eliminates head-of-line blocking — concurrent publishes proceed
// independently. AMQP channels are cheap (multiplexed over a single
// TCP connection) and designed for per-goroutine use.
type Publisher struct {
	conn   Connector
	logger *slog.Logger
}

// NewPublisher creates a Publisher bound to the given connection.
func NewPublisher(conn Connector, logger *slog.Logger) *Publisher {
	return &Publisher{conn: conn, logger: logger}
}

// Publish serializes and sends a Message to the specified exchange with the given routing key.
// It uses confirm mode to ensure the broker has acknowledged receipt of the message.
func (p *Publisher) Publish(ctx context.Context, exchange, routingKey string, msg messaging.Message) error {
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
	if len(msg.Headers) > 0 {
		pub.Headers = make(amqp.Table, len(msg.Headers))
		for k, v := range msg.Headers {
			pub.Headers[k] = v
		}
	}

	if err := p.publishConfirmed(ctx, exchange, routingKey, pub); err != nil {
		return err
	}

	p.logger.Debug("message published",
		"id", msg.ID,
		"type", msg.Type,
		"exchange", exchange,
		"routing_key", routingKey,
	)
	return nil
}

// PublishRaw sends pre-serialized bytes to an exchange with confirm mode.
// Use this for dead-letter forwarding where the original body should be
// preserved exactly (no re-serialization round-trip).
func (p *Publisher) PublishRaw(ctx context.Context, exchange, routingKey string, body []byte, msgID string) error {
	if err := p.publishConfirmed(ctx, exchange, routingKey, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		MessageId:    msgID,
		Body:         body,
	}); err != nil {
		return err
	}

	p.logger.Debug("raw message published",
		"id", msgID,
		"exchange", exchange,
		"routing_key", routingKey,
	)
	return nil
}

// publishConfirmed opens a dedicated channel, publishes, waits for the
// broker confirmation, and closes the channel. Each call is independent —
// no shared state, no mutex, no head-of-line blocking.
func (p *Publisher) publishConfirmed(ctx context.Context, exchange, routingKey string, pub amqp.Publishing) error {
	ch, err := p.conn.Channel()
	if err != nil {
		return fmt.Errorf("open channel: %w", err)
	}
	defer func() {
		if closeErr := ch.Close(); closeErr != nil {
			p.logger.Debug("failed to close publisher channel", "error", closeErr)
		}
	}()

	if err := ch.Confirm(false); err != nil {
		return fmt.Errorf("enable confirm mode: %w", err)
	}

	dc, err := ch.PublishWithDeferredConfirmWithContext(ctx,
		exchange, routingKey, false, false, pub,
	)
	if err != nil {
		return fmt.Errorf("publish message: %w", err)
	}

	acked, err := dc.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("wait for confirm: %w", err)
	}
	if !acked {
		return fmt.Errorf("message %s was nacked by broker", pub.MessageId)
	}

	return nil
}

// Close is a no-op — channels are opened and closed per-publish.
// Retained for interface compatibility.
func (p *Publisher) Close() {}
