package amqpbackend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/bds421/rho-kit/infra/v2/messaging"
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

// NewPublisher creates a Publisher bound to the given connection. Panics
// if conn is nil — the publisher dereferences it on every channel open,
// so accepting nil here would only defer the crash to the first publish.
// A nil logger is normalized to [slog.Default].
func NewPublisher(conn Connector, logger *slog.Logger) *Publisher {
	if conn == nil {
		panic("amqpbackend: NewPublisher requires a non-nil Connector")
	}
	if logger == nil {
		logger = slog.Default()
	}
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

// ErrUnroutable indicates the broker accepted the message but no queue was
// bound to receive it. Callers (outbox/buffered publisher) should treat this
// as a publish failure so the message is retried after topology is fixed,
// rather than silently lost.
var ErrUnroutable = errors.New("amqp: message returned by broker (no route to any queue)")

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
			p.logger.Debug("failed to close publisher channel", "error", closeErr)
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
		return fmt.Errorf("message %s was nacked by broker", pub.MessageId)
	}

	// Non-blocking check: if the broker returned the message as unroutable,
	// the amqp091-go reader has already delivered it to returnCh by the time
	// the ack arrives.
	select {
	case ret := <-returnCh:
		return fmt.Errorf("%w: id=%s exchange=%s routing_key=%s reply=%d %s",
			ErrUnroutable, pub.MessageId, ret.Exchange, ret.RoutingKey, ret.ReplyCode, ret.ReplyText)
	default:
		return nil
	}
}

// Close is a no-op — channels are opened and closed per-publish.
// Retained for interface compatibility.
func (p *Publisher) Close() {}
