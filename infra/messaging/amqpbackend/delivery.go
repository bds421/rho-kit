package amqpbackend

import (
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// fromAMQPDelivery creates a messaging.Delivery from a raw AMQP delivery
// and a decoded Message. String-valued AMQP headers are extracted into
// msg.Headers so handlers can access tracing metadata via msg.CorrelationID()
// and similar helpers. The schema version is read from X-Schema-Version header
// and propagated to both the Message and Delivery.
func fromAMQPDelivery(d amqp.Delivery, msg messaging.Message) messaging.Delivery {
	msg = msg.Clone()
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
// Negative values from untrusted AMQP int headers are clamped to 0 (unversioned).
func extractSchemaVersion(h amqp.Table, fallback uint) uint {
	if h == nil {
		return fallback
	}
	v, ok := h[messaging.HeaderSchemaVersion]
	if !ok {
		return fallback
	}
	switch n := v.(type) {
	case int32:
		return intToUint(int64(n))
	case int64:
		return intToUint(n)
	case int:
		return intToUint(int64(n))
	default:
		return fallback
	}
}

// intToUint converts a signed integer from an untrusted transport header to uint.
// Negative values are clamped to 0 (unversioned).
func intToUint(v int64) uint {
	if v < 0 {
		return 0
	}
	return uint(v)
}

// extractStringHeaders picks out string-valued AMQP headers for application-level
// tracing. Non-string values (x-death tables, etc.) are intentionally skipped.
// The output map is bounded by maxHeaderNodes AND maxHeaderBytes so a hostile
// peer cannot allocate an unbounded application-headers map upfront (L134).
// A peer that emits exactly maxHeaderNodes headers each carrying multi-MB
// values would otherwise still exhaust memory through the byte axis alone.
func extractStringHeaders(h amqp.Table) map[string]string {
	if len(h) == 0 {
		return nil
	}
	result := make(map[string]string)
	byteBudget := maxHeaderBytes
	for k, v := range h {
		if len(result) >= maxHeaderNodes {
			break
		}
		s, ok := v.(string)
		if !ok {
			continue
		}
		cost := len(k) + len(s)
		if cost > byteBudget {
			// Skip this oversize entry and keep scanning so map-iteration
			// order does not nondeterministically drop later small headers.
			continue
		}
		byteBudget -= cost
		result[k] = s
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// Header parsing bounds. AMQP delivery headers are attacker-controllable
// from any publisher with channel access, so a confused or hostile peer
// could send a deeply-nested or wide [amqp.Table] that stack-overflows
// the recursive walk or OOMs the consumer. We cap both axes — anything
// past the limits is dropped and replaced with a sentinel string so the
// consumer can still see the delivery for DLQ purposes without
// duplicating its own size enforcement.
const (
	maxHeaderDepth = 8
	maxHeaderNodes = 256
	// maxHeaderBytes caps the total aggregate name+value byte size of
	// materialised application headers per delivery. 64 KiB is generous
	// for realistic header sets (correlation IDs, trace IDs, tenant IDs,
	// timestamps) and matches the natsbackend cap (L134).
	maxHeaderBytes = 64 * 1024
)

// truncatedHeaderValue is the placeholder substituted for headers that
// exceed [maxHeaderDepth] or [maxHeaderNodes]. Operators alerting on
// this string see exactly when a peer exceeded the bound.
const truncatedHeaderValue = "<amqp-header-truncated>"

func headerToMap(h amqp.Table) map[string]any {
	if h == nil {
		return nil
	}
	budget := maxHeaderNodes
	return deepCopyTable(h, 0, &budget)
}

func deepCopyTable(src amqp.Table, depth int, budget *int) map[string]any {
	if depth >= maxHeaderDepth {
		return map[string]any{"": truncatedHeaderValue}
	}
	result := make(map[string]any, len(src))
	for k, v := range src {
		if *budget <= 0 {
			result[k] = truncatedHeaderValue
			continue
		}
		*budget--
		result[k] = deepCopyValue(v, depth+1, budget)
	}
	return result
}

func deepCopyValue(v any, depth int, budget *int) any {
	switch val := v.(type) {
	case amqp.Table:
		return deepCopyTable(val, depth, budget)
	case []any:
		if depth >= maxHeaderDepth {
			return truncatedHeaderValue
		}
		copied := make([]any, len(val))
		for i, item := range val {
			if *budget <= 0 {
				copied[i] = truncatedHeaderValue
				continue
			}
			*budget--
			copied[i] = deepCopyValue(item, depth+1, budget)
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
