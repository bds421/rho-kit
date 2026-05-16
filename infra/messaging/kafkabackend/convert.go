package kafkabackend

import (
	"encoding/json"
	"fmt"
	"strconv"

	kafka "github.com/segmentio/kafka-go"

	"github.com/bds421/rho-kit/infra/v2/messaging"
)

// Kafka record header keys used by this backend. The same set is
// emitted by the publisher and read by the subscriber so a round-trip
// through Kafka preserves the kit's [messaging.Message] envelope and
// the (exchange, routingKey) pair carried by [messaging.Delivery].
const (
	headerExchange   = "X-Exchange"
	headerRoutingKey = "X-Routing-Key"
	headerMessageID  = "X-Message-Id"
	headerMessageTyp = "X-Message-Type"
)

// maxConsumerDeliveryBytes caps the JSON-decoded record body the
// subscriber will hand to [json.Unmarshal]. Kafka's broker-side
// max.message.bytes is the first defence; this is the kit-side safety
// net against a misconfigured topic or a foreign-writer scenario.
// 32 MiB matches the kit's AMQP/NATS upper bound.
const maxConsumerDeliveryBytes = 32 * 1024 * 1024

// toKafkaMessage converts a [messaging.Message] into a kafka.Message.
// Exchange becomes the topic, routingKey becomes the record key (when
// non-empty), and the JSON-encoded Message becomes the Value. Each
// message header rides as a kafka.Header, plus X-Exchange,
// X-Routing-Key, X-Message-Id, X-Message-Type, and X-Schema-Version
// fixtures the subscriber relies on to reconstruct the
// [messaging.Delivery].
func toKafkaMessage(exchange, routingKey string, msg messaging.Message) (kafka.Message, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return kafka.Message{}, fmt.Errorf("kafkabackend: marshal message: %w", err)
	}
	headers := make([]kafka.Header, 0, len(msg.Headers)+5)
	for k, v := range msg.Headers {
		headers = append(headers, kafka.Header{Key: k, Value: []byte(v)})
	}
	headers = append(headers,
		kafka.Header{Key: headerExchange, Value: []byte(exchange)},
		kafka.Header{Key: headerRoutingKey, Value: []byte(routingKey)},
		kafka.Header{Key: headerMessageID, Value: []byte(msg.ID)},
		kafka.Header{Key: headerMessageTyp, Value: []byte(msg.Type)},
	)
	if msg.SchemaVersion != 0 {
		headers = append(headers, kafka.Header{
			Key:   messaging.HeaderSchemaVersion,
			Value: []byte(strconv.FormatUint(uint64(msg.SchemaVersion), 10)),
		})
	}
	km := kafka.Message{
		Topic:   exchange,
		Value:   body,
		Headers: headers,
	}
	if routingKey != "" {
		km.Key = []byte(routingKey)
	}
	return km, nil
}

// fromKafkaMessage decodes a kafka.Message into the kit's
// [messaging.Delivery]. Header-based metadata wins over inferred
// values; only when the producer did not stamp the X-Exchange /
// X-Routing-Key headers does the subscriber fall back to the record
// topic and key.
func fromKafkaMessage(km kafka.Message) (messaging.Delivery, error) {
	if len(km.Value) > maxConsumerDeliveryBytes {
		return messaging.Delivery{}, fmt.Errorf("kafkabackend: delivery exceeds %d bytes", maxConsumerDeliveryBytes)
	}
	var msg messaging.Message
	if err := json.Unmarshal(km.Value, &msg); err != nil {
		return messaging.Delivery{}, fmt.Errorf("kafkabackend: decode message body: %w", err)
	}
	headerAny, headerStr := splitHeaders(km.Headers)
	if msg.Headers == nil {
		msg.Headers = headerStr
	}
	exchange := stringHeader(km.Headers, headerExchange)
	if exchange == "" {
		exchange = km.Topic
	}
	routingKey := stringHeader(km.Headers, headerRoutingKey)
	if routingKey == "" {
		routingKey = string(km.Key)
	}
	schemaVersion := parseSchemaVersion(km.Headers)
	if schemaVersion != 0 {
		msg.SchemaVersion = schemaVersion
	}
	return messaging.Delivery{
		Message:       msg,
		Exchange:      exchange,
		RoutingKey:    routingKey,
		SchemaVersion: msg.SchemaVersion,
		Headers:       headerAny,
	}, nil
}

// splitHeaders materialises a Kafka header slice into both a
// map[string]any (for [messaging.Delivery.Headers]) and a
// map[string]string (for [messaging.Message.Headers]). Allocation is
// bounded — see [maxDeliveryHeaders] — so a hostile producer cannot
// force an unbounded map.
func splitHeaders(headers []kafka.Header) (map[string]any, map[string]string) {
	if len(headers) == 0 {
		return nil, nil
	}
	anyHeaders := make(map[string]any)
	strHeaders := make(map[string]string)
	byteBudget := maxDeliveryHeaderBytes
	for _, h := range headers {
		if len(anyHeaders) >= maxDeliveryHeaders {
			break
		}
		if h.Key == "" {
			continue
		}
		cost := len(h.Key) + len(h.Value)
		if cost > byteBudget {
			break
		}
		byteBudget -= cost
		v := string(h.Value)
		anyHeaders[h.Key] = v
		// Skip the kit-internal X-* envelope headers in the per-message
		// header map so the kit envelope and round-tripped Message.Headers
		// stay in sync with the publisher-side input.
		switch h.Key {
		case headerExchange, headerRoutingKey, headerMessageID, headerMessageTyp, messaging.HeaderSchemaVersion:
			continue
		}
		strHeaders[h.Key] = v
	}
	if len(anyHeaders) == 0 {
		anyHeaders = nil
	}
	if len(strHeaders) == 0 {
		strHeaders = nil
	}
	return anyHeaders, strHeaders
}

func stringHeader(headers []kafka.Header, key string) string {
	for _, h := range headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}

func parseSchemaVersion(headers []kafka.Header) uint {
	raw := stringHeader(headers, messaging.HeaderSchemaVersion)
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v < 0 {
		return 0
	}
	return uint(v)
}

// maxDeliveryHeaders caps the number of header entries materialised
// from a Kafka record so a hostile peer cannot force unbounded
// allocations. Mirrors the AMQP / NATS budgets.
const maxDeliveryHeaders = 256

// maxDeliveryHeaderBytes caps the aggregate key+value bytes across all
// materialised headers; defends against a peer that emits exactly
// maxDeliveryHeaders headers each carrying multi-MB values.
const maxDeliveryHeaderBytes = 64 * 1024
