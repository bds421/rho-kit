package redisstream

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/id"
	"github.com/bds421/rho-kit/core/v2/redact"
)

const (
	// MaxMessageIDBytes caps optional idempotency IDs.
	MaxMessageIDBytes = 255
	// MaxMessageTypeBytes caps event type names.
	MaxMessageTypeBytes = 256
	// MaxBatchMessages caps batch publish and consumer read operations so
	// callers cannot build unbounded Redis command batches.
	MaxBatchMessages = 1024
	// MaxHeaderNameBytes caps stream header names at a portable size.
	MaxHeaderNameBytes = 128
	// MaxHeaderValueBytes caps each stream header value.
	MaxHeaderValueBytes = 8 * 1024
	// MaxHeaderCount caps the total number of header entries per
	// message. A single message with the maximum number of headers
	// each at the maximum value size still fits inside the kit's
	// 1 MiB default message ceiling, but unbounded counts could blow
	// out the consumer-side map allocation before any payload guard
	// fires (L051).
	MaxHeaderCount = 64
	// MaxTotalHeaderBytes caps the aggregate header name+value size
	// per message so a peer cannot smuggle a multi-MB blob across
	// MaxHeaderCount entries that each individually fit under
	// MaxHeaderValueBytes (L051).
	MaxTotalHeaderBytes = 256 * 1024
)

// ErrInvalidMessage marks Redis Stream messages whose metadata or payload
// cannot safely be stored, logged, or dispatched to handlers.
var ErrInvalidMessage = errors.New("redisstream: invalid message")

// ErrMessageTooLarge marks stream messages rejected before publish or dispatch
// because their payload exceeds the configured size policy.
var ErrMessageTooLarge = errors.New("redisstream: message exceeds max payload size")

// ErrBatchTooLarge marks batch publish calls rejected before they reach Redis
// because they contain too many messages.
var ErrBatchTooLarge = errors.New("redisstream: batch operation exceeds maximum message count")

// ErrInvalidHeader marks Redis Stream message headers that cannot safely be
// stored or bridged to other message transports.
var ErrInvalidHeader = errors.New("redisstream: invalid message header")

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

// MessageTooLargeError reports the measured stream payload size and the
// configured limit.
type MessageTooLargeError struct {
	Size  int
	Limit int
}

func (e *MessageTooLargeError) Error() string {
	return fmt.Sprintf("redisstream: payload size %d exceeds max %d", e.Size, e.Limit)
}

func (e *MessageTooLargeError) Unwrap() error { return ErrMessageTooLarge }

// NewMessage creates a Message with a UUID v7 ID and current timestamp.
func NewMessage(msgType string, payload any) (Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return Message{}, redact.WrapError("marshal payload", err)
	}
	msg := Message{
		ID:        id.New(),
		Type:      msgType,
		Payload:   data,
		Timestamp: time.Now().UTC(),
	}
	if err := ValidateMessage(msg, 0); err != nil {
		return Message{}, err
	}
	return msg, nil
}

// Clone returns a copy of m with mutable payload and header containers detached.
func (m Message) Clone() Message {
	if m.Payload != nil {
		m.Payload = append(m.Payload[:0:0], m.Payload...)
	}
	if m.Headers != nil {
		headers := make(map[string]string, len(m.Headers))
		for k, v := range m.Headers {
			headers[k] = v
		}
		m.Headers = headers
	}
	return m
}

// WithHeader returns a copy of the message with the header added. The
// original message is not modified (immutability). Returns
// ErrInvalidHeader when key or value contains characters that cannot
// safely round-trip through Redis Streams or transport bridges (null
// bytes, newlines, carriage returns, non-UTF-8, oversized).
//
// Callers forwarding header values from request input must handle the
// error rather than crash the request goroutine.
func (m Message) WithHeader(key, value string) (Message, error) {
	if err := validateHeader(key, value); err != nil {
		return Message{}, err
	}
	clone := m.Clone()
	if clone.Headers == nil {
		clone.Headers = make(map[string]string, 1)
	}
	clone.Headers[key] = value
	return clone, nil
}

// ValidateMessage rejects stream messages that cannot safely round-trip through
// Redis Streams and the kit's message-transport bridges. Pass maxPayloadBytes=0
// to disable the payload cap.
func ValidateMessage(msg Message, maxPayloadBytes int) error {
	if maxPayloadBytes < 0 {
		return fmt.Errorf("%w: max payload bytes must be >= 0", ErrInvalidMessage)
	}
	if msg.Type == "" {
		return fmt.Errorf("%w: type must not be empty", ErrInvalidMessage)
	}
	if len(msg.Type) > MaxMessageTypeBytes {
		return fmt.Errorf("%w: type exceeds maximum length", ErrInvalidMessage)
	}
	if containsInvalidMessageString(msg.Type) {
		return fmt.Errorf("%w: type contains invalid characters", ErrInvalidMessage)
	}
	if len(msg.ID) > MaxMessageIDBytes {
		return fmt.Errorf("%w: id exceeds maximum length", ErrInvalidMessage)
	}
	if containsInvalidMessageString(msg.ID) {
		return fmt.Errorf("%w: id contains invalid characters", ErrInvalidMessage)
	}
	if maxPayloadBytes > 0 && len(msg.Payload) > maxPayloadBytes {
		return &MessageTooLargeError{Size: len(msg.Payload), Limit: maxPayloadBytes}
	}
	if len(msg.Payload) > 0 && !json.Valid(msg.Payload) {
		return fmt.Errorf("%w: payload must be valid JSON", ErrInvalidMessage)
	}
	if err := ValidateHeaders(msg.Headers); err != nil {
		return err
	}
	return nil
}

// ValidateHeaders rejects header metadata that cannot safely round-trip through
// Redis Streams and the kit's message-transport bridges.
func ValidateHeaders(headers map[string]string) error {
	if len(headers) > MaxHeaderCount {
		return fmt.Errorf("%w: header count %d exceeds maximum %d", ErrInvalidHeader, len(headers), MaxHeaderCount)
	}
	total := 0
	for name, value := range headers {
		if err := validateHeader(name, value); err != nil {
			return err
		}
		total += len(name) + len(value)
		if total > MaxTotalHeaderBytes {
			return fmt.Errorf("%w: total header bytes exceeds maximum %d", ErrInvalidHeader, MaxTotalHeaderBytes)
		}
	}
	return nil
}

func containsInvalidMessageString(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

func validateHeader(name, value string) error {
	if name == "" {
		return fmt.Errorf("%w: name must not be empty", ErrInvalidHeader)
	}
	if len(name) > MaxHeaderNameBytes {
		return fmt.Errorf("%w: name exceeds maximum length", ErrInvalidHeader)
	}
	for i := 0; i < len(name); i++ {
		if !isHeaderNameByte(name[i]) {
			return fmt.Errorf("%w: name contains invalid character", ErrInvalidHeader)
		}
	}
	if len(value) > MaxHeaderValueBytes {
		return fmt.Errorf("%w: value exceeds maximum length", ErrInvalidHeader)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: value contains invalid UTF-8", ErrInvalidHeader)
	}
	// Reject the full C0 control range plus DEL, matching ID/Type validation
	// via unicode.IsControl. Headers may be forwarded into logs and other
	// transports; accepting ESC/etc. enables log/terminal injection.
	for _, r := range value {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: value contains invalid character", ErrInvalidHeader)
		}
	}
	return nil
}

func isHeaderNameByte(c byte) bool {
	switch {
	case 'a' <= c && c <= 'z':
		return true
	case 'A' <= c && c <= 'Z':
		return true
	case '0' <= c && c <= '9':
		return true
	}
	switch c {
	case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
		return true
	default:
		return false
	}
}
