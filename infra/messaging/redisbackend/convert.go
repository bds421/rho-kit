package redisbackend

import (
	"strconv"

	stream "github.com/bds421/rho-kit/data/stream/redisstream/v2"
	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// toStreamMessage converts a messaging.Message to a stream.Message.
// SchemaVersion is propagated as a header field for transport.
func toStreamMessage(msg messaging.Message) stream.Message {
	extraHeaders := 0
	if msg.SchemaVersion != 0 {
		extraHeaders = 1
	}
	headers := make(map[string]string, len(msg.Headers)+extraHeaders)
	for k, v := range msg.Headers {
		headers[k] = v
	}
	if msg.SchemaVersion != 0 {
		headers[messaging.HeaderSchemaVersion] = strconv.FormatUint(uint64(msg.SchemaVersion), 10)
	}

	return stream.Message{
		ID:        msg.ID,
		Type:      msg.Type,
		Payload:   cloneRawMessage(msg.Payload),
		Timestamp: msg.Timestamp,
		Headers:   headers,
	}
}

// toDelivery converts a stream.Message into a messaging.Delivery.
// SchemaVersion is extracted from the transport header if present. The
// messaging routing key is restored from the transport header set by Publish;
// direct/legacy Redis stream messages fall back to the event type.
//
// Inbound headers/ID/type are validated with the same caps as Publish so a
// hostile stream writer cannot inject CRLF/control characters or unbounded
// header maps into handler Delivery values.
func toDelivery(sm stream.Message, streamName string) (messaging.Delivery, error) {
	messageHeaders := cloneStringHeaders(sm.Headers)
	// Strip kit-internal transport headers from the application-visible
	// Message.Headers (they are re-surfaced on Delivery fields below).
	delete(messageHeaders, headerRoutingKey)
	delete(messageHeaders, messaging.HeaderSchemaVersion)
	if err := messaging.ValidateMessageHeaders(messageHeaders); err != nil {
		return messaging.Delivery{}, err
	}
	msg := messaging.Message{
		ID:        sm.ID,
		Type:      sm.Type,
		Payload:   cloneRawMessage(sm.Payload),
		Timestamp: sm.Timestamp,
		Headers:   messageHeaders,
	}
	// Validate ID/type token rules; payload size is already enforced by the
	// stream consumer. Use ValidateMessage which also covers headers.
	if err := messaging.ValidateMessage(msg); err != nil {
		return messaging.Delivery{}, err
	}

	headers := make(map[string]any, len(messageHeaders))
	for k, v := range messageHeaders {
		headers[k] = v
	}
	schemaVersion := parseSchemaVersion(sm.Headers)
	msg.SchemaVersion = schemaVersion
	routingKey := sm.Headers[headerRoutingKey]
	if routingKey == "" {
		routingKey = sm.Type
	} else if err := messaging.ValidateRoutingKey(routingKey); err != nil {
		// Untrusted stream writer: refuse spoofed/invalid routing keys.
		return messaging.Delivery{}, err
	}
	corrID := messageHeaders[messaging.HeaderCorrelationID]

	return messaging.Delivery{
		Message:       msg,
		Exchange:      streamName,
		RoutingKey:    routingKey,
		SchemaVersion: schemaVersion,
		CorrelationID: corrID,
		Headers:       headers,
	}, nil
}

// parseSchemaVersion extracts the schema version from string headers.
// Returns 0 if the header is absent, cannot be parsed, or is negative
// (untrusted transport boundary: the raw string may represent a negative number).
func parseSchemaVersion(headers map[string]string) uint {
	raw, ok := headers[messaging.HeaderSchemaVersion]
	if !ok {
		return 0
	}
	// Bit-size of uint so out-of-range values fail closed to 0 on 32-bit
	// platforms instead of silently truncating onto a real version.
	v, err := strconv.ParseUint(raw, 10, strconv.IntSize)
	if err != nil {
		return 0
	}
	return uint(v)
}

func cloneRawMessage(payload []byte) []byte {
	if payload == nil {
		return nil
	}
	return append(payload[:0:0], payload...)
}

func cloneStringHeaders(headers map[string]string) map[string]string {
	if headers == nil {
		return nil
	}
	clone := make(map[string]string, len(headers))
	for k, v := range headers {
		clone[k] = v
	}
	return clone
}
