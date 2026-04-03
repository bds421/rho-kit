// Package wmconvert provides bidirectional conversion between rho-kit's
// messaging.Message / messaging.Delivery types and Watermill's message.Message,
// plus adapter types that wrap any Watermill publisher/subscriber behind
// rho-kit's messaging interfaces.
//
// # Conversion Functions
//
// [ToWatermill] and [FromWatermill] convert between the two message types.
// [ToDelivery] creates a full messaging.Delivery with transport metadata.
//
// # Adapter Types
//
// [PublisherAdapter] wraps watermill.Publisher → messaging.MessagePublisher.
// [ConsumerAdapter] wraps watermill.Subscriber → messaging.MessageConsumer.
// [ConnectorAdapter] wraps health/close functions → messaging.Connector.
//
// # Backend
//
// [Backend] combines all three adapters for convenient wiring. Use [NewBackend]
// with any Watermill publisher/subscriber pair (Kafka, NATS, Google Pub/Sub,
// SQL, GoChannel, etc.) to get rho-kit messaging interfaces.
//
//	backend := wmconvert.NewBackend(kafkaPub, kafkaSub, logger)
//	pub := backend.Publisher()    // messaging.MessagePublisher
//	con := backend.Consumer()     // messaging.MessageConsumer
//	conn := backend.Connector()   // messaging.Connector
package wmconvert

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/bds421/rho-kit/infra/messaging"
)

// Metadata keys used to carry rho-kit message fields through Watermill metadata.
const (
	MetaMessageType   = "rho_message_type"
	MetaTimestamp     = "rho_timestamp"
	MetaSchemaVersion = "rho_schema_version"
	MetaExchange      = "rho_exchange"
	MetaRoutingKey    = "rho_routing_key"
	MetaReplyTo       = "rho_reply_to"
	MetaCorrelationID = "rho_correlation_id"
	MetaRedelivered   = "rho_redelivered"
)

// ToWatermill converts a rho-kit messaging.Message into a Watermill message.
// The exchange and routingKey are stored in metadata so the publisher adapter
// can route them to the correct topic/exchange.
func ToWatermill(msg messaging.Message, exchange, routingKey string) *message.Message {
	wmMsg := message.NewMessage(msg.ID, message.Payload(msg.Payload))

	wmMsg.Metadata.Set(MetaMessageType, msg.Type)
	wmMsg.Metadata.Set(MetaTimestamp, msg.Timestamp.Format(time.RFC3339Nano))

	if msg.SchemaVersion != 0 {
		wmMsg.Metadata.Set(MetaSchemaVersion, strconv.FormatUint(uint64(msg.SchemaVersion), 10))
	}

	if exchange != "" {
		wmMsg.Metadata.Set(MetaExchange, exchange)
	}
	if routingKey != "" {
		wmMsg.Metadata.Set(MetaRoutingKey, routingKey)
	}

	for k, v := range msg.Headers {
		wmMsg.Metadata.Set(k, v)
	}

	return wmMsg
}

// FromWatermill converts a Watermill message back into a rho-kit messaging.Message.
// Returns an error if required metadata (message type) is missing.
func FromWatermill(wmMsg *message.Message) (messaging.Message, error) {
	msgType := wmMsg.Metadata.Get(MetaMessageType)
	if msgType == "" {
		return messaging.Message{}, fmt.Errorf("wmconvert: missing %s metadata", MetaMessageType)
	}

	ts := time.Now().UTC()
	if raw := wmMsg.Metadata.Get(MetaTimestamp); raw != "" {
		parsed, err := time.Parse(time.RFC3339Nano, raw)
		if err == nil {
			ts = parsed
		}
	}

	var schemaVersion uint
	if raw := wmMsg.Metadata.Get(MetaSchemaVersion); raw != "" {
		v, err := strconv.ParseUint(raw, 10, 64)
		if err == nil {
			schemaVersion = uint(v)
		}
	}

	headers := extractHeaders(wmMsg.Metadata)

	return messaging.Message{
		ID:            wmMsg.UUID,
		Type:          msgType,
		Payload:       json.RawMessage(wmMsg.Payload),
		Timestamp:     ts,
		SchemaVersion: schemaVersion,
		Headers:       headers,
	}, nil
}

// ToDelivery converts a Watermill message into a rho-kit messaging.Delivery.
// It extracts transport metadata (exchange, routing key, etc.) from the
// Watermill message metadata.
func ToDelivery(wmMsg *message.Message) (messaging.Delivery, error) {
	msg, err := FromWatermill(wmMsg)
	if err != nil {
		return messaging.Delivery{}, err
	}

	return messaging.Delivery{
		Message:       msg,
		ReplyTo:       wmMsg.Metadata.Get(MetaReplyTo),
		CorrelationID: wmMsg.Metadata.Get(MetaCorrelationID),
		Exchange:      wmMsg.Metadata.Get(MetaExchange),
		RoutingKey:    wmMsg.Metadata.Get(MetaRoutingKey),
		SchemaVersion: msg.SchemaVersion,
		Redelivered:   wmMsg.Metadata.Get(MetaRedelivered) == "true",
		Headers:       toAnyHeaders(extractHeaders(wmMsg.Metadata)),
	}, nil
}

// reservedKeys are metadata keys that carry rho-kit structural fields
// and should not be included in the user-facing Headers map.
var reservedKeys = map[string]bool{
	MetaMessageType:   true,
	MetaTimestamp:     true,
	MetaSchemaVersion: true,
	MetaExchange:      true,
	MetaRoutingKey:    true,
	MetaReplyTo:       true,
	MetaCorrelationID: true,
	MetaRedelivered:   true,
}

// extractHeaders returns all metadata entries that are not reserved rho-kit
// structural fields.
func extractHeaders(md message.Metadata) map[string]string {
	if len(md) == 0 {
		return nil
	}
	headers := make(map[string]string, len(md))
	for k, v := range md {
		if !reservedKeys[k] {
			headers[k] = v
		}
	}
	if len(headers) == 0 {
		return nil
	}
	return headers
}

// toAnyHeaders converts string headers to map[string]any for Delivery.Headers.
func toAnyHeaders(headers map[string]string) map[string]any {
	if headers == nil {
		return nil
	}
	result := make(map[string]any, len(headers))
	for k, v := range headers {
		result[k] = v
	}
	return result
}
