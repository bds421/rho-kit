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
func toDelivery(sm stream.Message, streamName string) messaging.Delivery {
	headers := make(map[string]any, len(sm.Headers))
	for k, v := range sm.Headers {
		headers[k] = v
	}
	messageHeaders := cloneStringHeaders(sm.Headers)

	schemaVersion := parseSchemaVersion(sm.Headers)
	routingKey := sm.Headers[headerRoutingKey]
	if routingKey == "" {
		routingKey = sm.Type
	}

	return messaging.Delivery{
		Message: messaging.Message{
			ID:            sm.ID,
			Type:          sm.Type,
			Payload:       cloneRawMessage(sm.Payload),
			Timestamp:     sm.Timestamp,
			SchemaVersion: schemaVersion,
			Headers:       messageHeaders,
		},
		Exchange:      streamName,
		RoutingKey:    routingKey,
		SchemaVersion: schemaVersion,
		Headers:       headers,
	}
}

// parseSchemaVersion extracts the schema version from string headers.
// Returns 0 if the header is absent, cannot be parsed, or is negative
// (untrusted transport boundary: the raw string may represent a negative number).
func parseSchemaVersion(headers map[string]string) uint {
	raw, ok := headers[messaging.HeaderSchemaVersion]
	if !ok {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
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
