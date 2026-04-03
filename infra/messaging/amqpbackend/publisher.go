package amqpbackend

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"

	amqp "github.com/rabbitmq/amqp091-go"

	watermillAMQP "github.com/ThreeDotsLabs/watermill-amqp/v3/pkg/amqp"
	"github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/bds421/rho-kit/infra/messaging"
)

// Publisher sends messages to AMQP exchanges via Watermill.
//
// For standard messaging.Message publishing, the Watermill pipeline handles
// channel management, confirm mode, and reconnection. For dead-letter
// forwarding (raw bytes), PublishRaw bypasses Watermill and uses a direct
// AMQP channel.
type Publisher struct {
	wmPub  *watermillAMQP.Publisher
	conn   Connector // retained for PublishRaw (direct channel access)
	logger *slog.Logger
}

// NewPublisher creates a Publisher backed by Watermill-AMQP.
// The Connection is used for PublishRaw (direct channel operations).
// Watermill manages its own AMQP connection internally with reconnection.
func NewPublisher(conn *Connection, logger *slog.Logger) *Publisher {
	wmCfg := watermillAMQP.NewDurablePubSubConfig(
		conn.url,
		watermillAMQP.GenerateQueueNameTopicName,
	)
	wmCfg.Publish.ConfirmDelivery = true
	wmCfg.Publish.ChannelPoolSize = 4
	wmCfg.Marshaler = Marshaler{}

	if conn.tlsConfig != nil {
		wmCfg.Connection.TLSConfig = conn.tlsConfig.Clone()
	}
	wmCfg.Connection.AmqpURI = conn.url

	wmPub, err := watermillAMQP.NewPublisher(wmCfg, watermill.NewSlogLogger(logger))
	if err != nil {
		logger.Error("failed to create watermill-amqp publisher, publish will fail", "error", err)
	}

	return &Publisher{
		wmPub:  wmPub,
		conn:   conn,
		logger: logger,
	}
}

// Publish serializes and sends a Message to the specified exchange with the given routing key.
// It uses Watermill-AMQP with confirm mode to ensure the broker has acknowledged receipt.
func (p *Publisher) Publish(ctx context.Context, exchange, routingKey string, msg messaging.Message) error {
	body, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	wmMsg := message.NewMessage(msg.ID, body)
	wmMsg.SetContext(ctx)

	// Set routing metadata that the Marshaler includes in AMQP headers.
	for k, v := range msg.Headers {
		wmMsg.Metadata.Set(k, v)
	}
	if msg.SchemaVersion != 0 {
		sv := msg.SchemaVersion
		if sv > math.MaxInt32 {
			sv = math.MaxInt32
		}
		wmMsg.Metadata.Set(messaging.HeaderSchemaVersion, fmt.Sprintf("%d", sv))
	}

	// Watermill topic = exchange. Routing key is encoded in the publishing.
	// We set it via the GenerateRoutingKey config or directly.
	wmMsg.Metadata.Set("_amqp_routing_key", routingKey)

	if err := p.wmPub.Publish(exchange, wmMsg); err != nil {
		return fmt.Errorf("publish message: %w", err)
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
//
// PublishRaw bypasses Watermill and uses a direct AMQP channel because
// Watermill does not support raw byte publishing without marshaling.
func (p *Publisher) PublishRaw(ctx context.Context, exchange, routingKey string, body []byte, msgID string) error {
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
		exchange, routingKey, false, false, amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			MessageId:    msgID,
			Body:         body,
		},
	)
	if err != nil {
		return fmt.Errorf("publish message: %w", err)
	}

	acked, err := dc.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("wait for confirm: %w", err)
	}
	if !acked {
		return fmt.Errorf("message %s was nacked by broker", msgID)
	}

	p.logger.Debug("raw message published",
		"id", msgID,
		"exchange", exchange,
		"routing_key", routingKey,
	)
	return nil
}

// Close closes the underlying Watermill publisher.
func (p *Publisher) Close() {
	if p.wmPub != nil {
		if err := p.wmPub.Close(); err != nil {
			p.logger.Debug("failed to close watermill publisher", "error", err)
		}
	}
}
