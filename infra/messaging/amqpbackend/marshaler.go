package amqpbackend

import (
	"encoding/json"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/ThreeDotsLabs/watermill/message"

	"github.com/bds421/rho-kit/infra/messaging"
)

// Marshaler converts between rho-kit messaging.Message (serialized as the AMQP
// body) and Watermill message.Message. It preserves all string-valued AMQP
// headers as Watermill metadata and silently skips non-string headers (e.g.
// x-death tables) to avoid unmarshalling errors.
type Marshaler struct{}

// Marshal converts a Watermill message into an AMQP publishing.
// The Watermill payload is used as-is (it is already a JSON-serialized
// messaging.Message). Metadata entries are written as AMQP string headers.
func (Marshaler) Marshal(wmMsg *message.Message) (amqp.Publishing, error) {
	headers := make(amqp.Table, len(wmMsg.Metadata)+1)
	for k, v := range wmMsg.Metadata {
		headers[k] = v
	}
	headers["_watermill_message_uuid"] = wmMsg.UUID

	return amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         wmMsg.Payload,
		Headers:      headers,
	}, nil
}

// Unmarshal converts an AMQP delivery into a Watermill message.
// String-valued headers become Watermill metadata. Non-string headers (e.g.
// x-death AMQP tables) are silently skipped — they cannot be represented in
// Watermill's string-only metadata model and would cause errors in the
// default Watermill marshaler.
//
// The raw AMQP delivery headers (including non-string values) are accessible
// via the "amqp_raw_headers" metadata key as JSON, enabling middleware that
// needs transport-level headers (e.g. x-death counting).
func (Marshaler) Unmarshal(pub amqp.Delivery) (*message.Message, error) {
	msgID := ""
	metadata := make(message.Metadata)

	for k, v := range pub.Headers {
		if k == "_watermill_message_uuid" {
			if s, ok := v.(string); ok {
				msgID = s
			}
			continue
		}
		if s, ok := v.(string); ok {
			metadata[k] = s
		}
		// Non-string headers (x-death tables, int headers) are silently
		// skipped. They are available via the raw headers below.
	}

	// Store raw headers as JSON for middleware that needs them.
	if len(pub.Headers) > 0 {
		raw, err := json.Marshal(headerTableToMap(pub.Headers))
		if err == nil {
			metadata["_amqp_raw_headers"] = string(raw)
		}
	}

	// Propagate AMQP delivery metadata.
	if pub.Exchange != "" {
		metadata["_amqp_exchange"] = pub.Exchange
	}
	if pub.RoutingKey != "" {
		metadata["_amqp_routing_key"] = pub.RoutingKey
	}
	if pub.ReplyTo != "" {
		metadata["_amqp_reply_to"] = pub.ReplyTo
	}
	if pub.CorrelationId != "" {
		metadata["_amqp_correlation_id"] = pub.CorrelationId
	}
	if pub.Redelivered {
		metadata["_amqp_redelivered"] = "true"
	}

	// Fall back to AMQP MessageId if no Watermill UUID header was present
	// (interop with non-Watermill producers).
	if msgID == "" {
		msgID = pub.MessageId
	}
	if msgID == "" {
		// Decode from body as last resort (rho-kit messages have .id in JSON).
		var envelope struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(pub.Body, &envelope); err == nil && envelope.ID != "" {
			msgID = envelope.ID
		}
	}

	wmMsg := message.NewMessage(msgID, pub.Body)
	wmMsg.Metadata = metadata
	return wmMsg, nil
}

// headerTableToMap converts AMQP Table to a JSON-safe map.
// AMQP tables can contain nested tables and typed values that json.Marshal
// handles natively (int, string, bool, etc.).
func headerTableToMap(t amqp.Table) map[string]any {
	result := make(map[string]any, len(t))
	for k, v := range t {
		switch val := v.(type) {
		case amqp.Table:
			result[k] = headerTableToMap(val)
		case []any:
			converted := make([]any, len(val))
			for i, item := range val {
				if tbl, ok := item.(amqp.Table); ok {
					converted[i] = headerTableToMap(tbl)
				} else {
					converted[i] = item
				}
			}
			result[k] = converted
		default:
			result[k] = v
		}
	}
	return result
}

// Compile-time check.
var _ interface {
	Marshal(*message.Message) (amqp.Publishing, error)
	Unmarshal(amqp.Delivery) (*message.Message, error)
} = Marshaler{}

// RawHeadersFromMetadata extracts the raw AMQP headers from Watermill message
// metadata. Returns nil if no raw headers are available. This is useful for
// middleware that needs access to non-string AMQP headers (e.g. x-death).
func RawHeadersFromMetadata(md message.Metadata) map[string]any {
	raw := md.Get("_amqp_raw_headers")
	if raw == "" {
		return nil
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil
	}
	return result
}

// DeliveryFromWatermillMessage constructs a messaging.Delivery from a Watermill
// message that was unmarshalled by [Marshaler]. This extracts AMQP-specific
// metadata (exchange, routing key, reply-to, etc.) from the Watermill metadata.
func DeliveryFromWatermillMessage(wmMsg *message.Message) (messaging.Delivery, error) {
	var msg messaging.Message
	if err := json.Unmarshal(wmMsg.Payload, &msg); err != nil {
		return messaging.Delivery{}, fmt.Errorf("unmarshal message body: %w", err)
	}

	// Extract string headers from metadata (skip internal keys).
	headers := make(map[string]string)
	for k, v := range wmMsg.Metadata {
		if len(k) > 0 && k[0] == '_' {
			continue // skip internal metadata keys
		}
		headers[k] = v
	}
	if len(headers) > 0 {
		msg.Headers = headers
	}

	// Extract schema version from headers if present.
	schemaVersion := extractSchemaVersionFromMetadata(wmMsg.Metadata)
	if schemaVersion > 0 {
		msg.SchemaVersion = schemaVersion
	}

	redelivered := wmMsg.Metadata.Get("_amqp_redelivered") == "true"

	// Build raw headers map for Delivery.Headers (includes non-string values).
	rawHeaders := RawHeadersFromMetadata(wmMsg.Metadata)

	return messaging.Delivery{
		Message:       msg,
		ReplyTo:       wmMsg.Metadata.Get("_amqp_reply_to"),
		CorrelationID: wmMsg.Metadata.Get("_amqp_correlation_id"),
		Exchange:      wmMsg.Metadata.Get("_amqp_exchange"),
		RoutingKey:    wmMsg.Metadata.Get("_amqp_routing_key"),
		SchemaVersion: msg.SchemaVersion,
		Redelivered:   redelivered,
		Headers:       rawHeaders,
	}, nil
}

func extractSchemaVersionFromMetadata(md message.Metadata) uint {
	raw := md.Get(messaging.HeaderSchemaVersion)
	if raw == "" {
		return 0
	}
	var v uint
	_, _ = fmt.Sscanf(raw, "%d", &v)
	return v
}
