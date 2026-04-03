package redisbackend

import (
	"strconv"

	stream "github.com/bds421/rho-kit/data/stream/redisstream"
	"github.com/bds421/rho-kit/infra/messaging"
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
		Payload:   msg.Payload,
		Timestamp: msg.Timestamp,
		Headers:   headers,
	}
}

// toDelivery converts a stream.Message into a messaging.Delivery.
// SchemaVersion is extracted from the transport header if present.
func toDelivery(sm stream.Message, streamName string) messaging.Delivery {
	headers := make(map[string]any, len(sm.Headers))
	for k, v := range sm.Headers {
		headers[k] = v
	}

	schemaVersion := parseSchemaVersion(sm.Headers)

	return messaging.Delivery{
		Message: messaging.Message{
			ID:            sm.ID,
			Type:          sm.Type,
			Payload:       sm.Payload,
			Timestamp:     sm.Timestamp,
			SchemaVersion: schemaVersion,
			Headers:       sm.Headers,
		},
		Exchange:      streamName,
		RoutingKey:    sm.Type,
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
