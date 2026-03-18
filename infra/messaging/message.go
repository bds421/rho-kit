package messaging

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Header keys for cross-service tracing.
const (
	HeaderCorrelationID = "X-Correlation-Id"
	HeaderRequestID     = "X-Request-Id"
)

// Message represents a structured RabbitMQ message with metadata.
type Message struct {
	ID        string          `json:"id"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload"`
	Timestamp time.Time       `json:"timestamp"`

	// Headers are propagated as AMQP headers for cross-service tracing.
	// Not serialized into the JSON body — carried as AMQP transport metadata.
	Headers map[string]string `json:"-"`
}

// NewMessage creates a Message with a UUID v7 ID and current timestamp.
func NewMessage(msgType string, payload any) (Message, error) {
	if msgType == "" {
		return Message{}, fmt.Errorf("message type must not be empty")
	}

	data, err := json.Marshal(payload)
	if err != nil {
		return Message{}, fmt.Errorf("marshal payload: %w", err)
	}

	id, err := uuid.NewV7()
	if err != nil {
		return Message{}, fmt.Errorf("generate message id: %w", err)
	}

	return Message{
		ID:        id.String(),
		Type:      msgType,
		Payload:   data,
		Timestamp: time.Now().UTC(),
	}, nil
}

// WithHeader returns a copy of the message with the given header set.
func (m Message) WithHeader(key, value string) Message {
	headers := make(map[string]string, len(m.Headers)+1)
	for k, v := range m.Headers {
		headers[k] = v
	}
	headers[key] = value
	return Message{
		ID:        m.ID,
		Type:      m.Type,
		Payload:   m.Payload,
		Timestamp: m.Timestamp,
		Headers:   headers,
	}
}

// CorrelationID returns the correlation ID from headers, or empty string.
func (m Message) CorrelationID() string {
	return m.Headers[HeaderCorrelationID]
}

// DecodePayload unmarshals the message payload into the provided target.
func (m Message) DecodePayload(target any) error {
	if err := json.Unmarshal(m.Payload, target); err != nil {
		return fmt.Errorf("decode payload for message %s: %w", m.ID, err)
	}
	return nil
}
