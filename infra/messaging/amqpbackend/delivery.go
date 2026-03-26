package amqpbackend

import (
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/bds421/rho-kit/infra/messaging"
)

// fromAMQPDelivery creates a messaging.Delivery from a raw AMQP delivery
// and a decoded Message. String-valued AMQP headers are extracted into
// msg.Headers so handlers can access tracing metadata via msg.CorrelationID()
// and similar helpers. The schema version is read from X-Schema-Version header
// and propagated to both the Message and Delivery.
func fromAMQPDelivery(d amqp.Delivery, msg messaging.Message) messaging.Delivery {
	msg.Headers = extractStringHeaders(d.Headers)
	schemaVersion := extractSchemaVersion(d.Headers, msg.SchemaVersion)
	msg.SchemaVersion = schemaVersion
	return messaging.Delivery{
		Message:       msg,
		ReplyTo:       d.ReplyTo,
		CorrelationID: d.CorrelationId,
		Exchange:      d.Exchange,
		RoutingKey:    d.RoutingKey,
		SchemaVersion: schemaVersion,
		Redelivered:   d.Redelivered,
		Headers:       headerToMap(d.Headers),
	}
}

// extractSchemaVersion reads the schema version from AMQP headers.
// If the header is absent, the fallback value (typically from the JSON body) is used.
// Negative values from untrusted headers are clamped to 0 (unversioned).
func extractSchemaVersion(h amqp.Table, fallback int) int {
	if h == nil {
		return clampVersion(fallback)
	}
	v, ok := h[messaging.HeaderSchemaVersion]
	if !ok {
		return clampVersion(fallback)
	}
	switch n := v.(type) {
	case int32:
		return clampVersion(int(n))
	case int64:
		return clampVersion(int(n))
	case int:
		return clampVersion(n)
	default:
		return clampVersion(fallback)
	}
}

// clampVersion ensures a schema version is never negative.
func clampVersion(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

// extractStringHeaders picks out string-valued AMQP headers for application-level
// tracing. Non-string values (x-death tables, etc.) are intentionally skipped.
func extractStringHeaders(h amqp.Table) map[string]string {
	if len(h) == 0 {
		return nil
	}
	result := make(map[string]string)
	for k, v := range h {
		if s, ok := v.(string); ok {
			result[k] = s
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func headerToMap(h amqp.Table) map[string]any {
	if h == nil {
		return nil
	}
	return deepCopyTable(h)
}

func deepCopyTable(src amqp.Table) map[string]any {
	result := make(map[string]any, len(src))
	for k, v := range src {
		result[k] = deepCopyValue(v)
	}
	return result
}

func deepCopyValue(v any) any {
	switch val := v.(type) {
	case amqp.Table:
		return deepCopyTable(val)
	case []any:
		copied := make([]any, len(val))
		for i, item := range val {
			copied[i] = deepCopyValue(item)
		}
		return copied
	case []byte:
		copied := make([]byte, len(val))
		copy(copied, val)
		return copied
	default:
		return v
	}
}
