package redisstream

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Message represents a message to be published to a Redis stream.
type Message struct {
	ID        string            // unique message ID (UUID v7 if empty)
	Type      string            // event type, e.g. "orders.created"
	Payload   json.RawMessage   // message body
	Headers   map[string]string // metadata (correlation ID, trace ID, etc.)
	Timestamp time.Time         // UTC creation time (auto-set if zero)

	// RedisStreamID is the server-assigned stream entry ID (e.g. "1234567890-0").
	// Set by the consumer when reading from a stream; empty when publishing.
	// Useful for logging, debugging, and idempotency checks in handlers.
	RedisStreamID string `json:"-"` // exclude from JSON serialization
}

// NewMessage creates a Message with a UUID v7 ID and current timestamp.
func NewMessage(msgType string, payload any) (Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return Message{}, fmt.Errorf("marshal payload: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Message{}, fmt.Errorf("generate message ID: %w", err)
	}
	return Message{
		ID:        id.String(),
		Type:      msgType,
		Payload:   data,
		Timestamp: time.Now().UTC(),
	}, nil
}

// WithHeader returns a copy of the message with the header added.
// The original message is not modified (immutability).
// Panics if key or value contains null bytes, newlines, or carriage returns.
func (m Message) WithHeader(key, value string) Message {
	if key == "" || strings.ContainsAny(key, "\x00\n\r") {
		panic("redis: header key must not be empty or contain null bytes/newlines")
	}
	if strings.ContainsAny(value, "\x00\n\r") {
		panic("redis: header value must not contain null bytes/newlines")
	}
	headers := make(map[string]string, len(m.Headers)+1)
	for k, v := range m.Headers {
		headers[k] = v
	}
	headers[key] = value
	return Message{
		ID:        m.ID,
		Type:      m.Type,
		Payload:   m.Payload,
		Headers:   headers,
		Timestamp: m.Timestamp,
	}
}
