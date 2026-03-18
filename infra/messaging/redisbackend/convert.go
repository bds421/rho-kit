package redisbackend

import (
	"github.com/bds421/rho-kit/infra/messaging"
	stream "github.com/bds421/rho-kit/data/stream/redisstream"
)

// toStreamMessage converts a messaging.Message to a stream.Message.
func toStreamMessage(msg messaging.Message) stream.Message {
	headers := make(map[string]string, len(msg.Headers))
	for k, v := range msg.Headers {
		headers[k] = v
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
func toDelivery(sm stream.Message, streamName string) messaging.Delivery {
	headers := make(map[string]any, len(sm.Headers))
	for k, v := range sm.Headers {
		headers[k] = v
	}

	return messaging.Delivery{
		Message: messaging.Message{
			ID:        sm.ID,
			Type:      sm.Type,
			Payload:   sm.Payload,
			Timestamp: sm.Timestamp,
			Headers:   sm.Headers,
		},
		Exchange:   streamName,
		RoutingKey: sm.Type,
		Headers:    headers,
	}
}
