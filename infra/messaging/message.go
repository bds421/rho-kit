package messaging

import (
	"encoding/json"
	"fmt"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/bds421/rho-kit/core/v2/apperror"
	"github.com/google/uuid"
)

// Header keys for cross-service tracing and schema versioning.
const (
	HeaderCorrelationID = "X-Correlation-Id"
	HeaderRequestID     = "X-Request-Id"
	HeaderSchemaVersion = "X-Schema-Version"
)

const (
	// MaxMessageIDBytes caps message IDs used in logs, headers, and broker metadata.
	MaxMessageIDBytes = 255
	// MaxMessageTypeBytes caps message type names used in logs, routing fallbacks, and handler dispatch.
	MaxMessageTypeBytes = 256
	// MaxMessageHeaderNameBytes caps transport header names at a portable size.
	MaxMessageHeaderNameBytes = 128
	// MaxMessageHeaderValueBytes caps each transport header value.
	MaxMessageHeaderValueBytes = 8 * 1024
	// MaxMessageHeaders caps the per-message header-map entry count. The
	// per-entry size caps above bound each header's bytes, but without a
	// count cap a hostile peer can send 10^5 maximally-sized headers and
	// allocate ~800 MB at validation time. 64 is generous for any realistic
	// header set (tracing, correlation, tenant, content-type, etc.) and a
	// hard stop short of a DoS budget.
	MaxMessageHeaders = 64
)

// ErrInvalidMessage marks message metadata or payload that is not portable
// across the kit's AMQP, NATS, Redis, and in-memory messaging backends. It is
// an [apperror.ValidationError] so HTTP and gRPC adapters map it to
// 400/InvalidArgument automatically.
var ErrInvalidMessage = apperror.NewValidation("messaging: invalid message")

// ErrInvalidMessageHeader marks message headers that are not portable across
// the kit's AMQP, NATS, Redis, and in-memory messaging backends. It is an
// [apperror.ValidationError] so HTTP and gRPC adapters map it to
// 400/InvalidArgument automatically.
var ErrInvalidMessageHeader = apperror.NewValidation("messaging: invalid message header")

// Message represents a structured message with metadata.
// It is transport-agnostic and used by both AMQP and Redis backends.
type Message struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	Payload       json.RawMessage `json:"payload"`
	Timestamp     time.Time       `json:"timestamp"`
	SchemaVersion uint            `json:"schema_version,omitempty"`

	// Headers are propagated as transport-level metadata for cross-service tracing.
	// Not serialized into the JSON body — carried as transport metadata
	// (e.g. AMQP headers, Redis stream fields).
	Headers map[string]string `json:"-"`
}

// NewMessage creates a Message with a UUID v7 ID and current timestamp.
func NewMessage(msgType string, payload any) (Message, error) {
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
	}.validated()
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

// WithHeader returns a copy of the message with the given header set.
// Returns ErrInvalidMessageHeader when the key or value contains
// characters that cannot safely round-trip through transport bridges
// (null bytes, newlines, oversized). Callers forwarding header values
// from request input must handle the error rather than crash the
// request goroutine.
func (m Message) WithHeader(key, value string) (Message, error) {
	if err := ValidateMessageHeader(key, value); err != nil {
		return Message{}, err
	}
	clone := m.Clone()
	if clone.Headers == nil {
		clone.Headers = make(map[string]string, 1)
	}
	clone.Headers[key] = value
	return clone, nil
}

// WithSchemaVersion returns a copy of the message with the given schema version.
// Version 0 represents unversioned/legacy messages.
func (m Message) WithSchemaVersion(version uint) Message {
	clone := m.Clone()
	clone.SchemaVersion = version
	return clone
}

// CorrelationID returns the correlation ID from headers, or empty string.
func (m Message) CorrelationID() string {
	return m.Headers[HeaderCorrelationID]
}

// DecodePayload unmarshals the message payload into the provided target.
func (m Message) DecodePayload(target any) error {
	if err := json.Unmarshal(m.Payload, target); err != nil {
		return fmt.Errorf("decode payload: %w", err)
	}
	return nil
}

// ValidateMessage rejects message metadata and payloads that cannot safely
// round-trip through all supported brokers and observability surfaces.
func ValidateMessage(msg Message) error {
	if err := validateMessageToken("id", msg.ID, MaxMessageIDBytes); err != nil {
		return err
	}
	if err := validateMessageToken("type", msg.Type, MaxMessageTypeBytes); err != nil {
		return err
	}
	if msg.Payload != nil && !json.Valid(msg.Payload) {
		return fmt.Errorf("%w: payload must be valid JSON", ErrInvalidMessage)
	}
	if err := ValidateMessageHeaders(msg.Headers); err != nil {
		return err
	}
	return nil
}

// ValidateMessageHeaders rejects header metadata that cannot safely round-trip
// across every message transport supported by the kit.
func ValidateMessageHeaders(headers map[string]string) error {
	if len(headers) > MaxMessageHeaders {
		return fmt.Errorf("%w: header count exceeds %d", ErrInvalidMessageHeader, MaxMessageHeaders)
	}
	for name, value := range headers {
		if err := ValidateMessageHeader(name, value); err != nil {
			return err
		}
	}
	return nil
}

// ValidateMessageHeader rejects empty, oversized, non-token header names and
// values containing invalid UTF-8 or response-splitting control bytes.
func ValidateMessageHeader(name, value string) error {
	if name == "" {
		return fmt.Errorf("%w: name must not be empty", ErrInvalidMessageHeader)
	}
	if len(name) > MaxMessageHeaderNameBytes {
		return fmt.Errorf("%w: name exceeds maximum length", ErrInvalidMessageHeader)
	}
	for i := 0; i < len(name); i++ {
		if !isMessageHeaderNameByte(name[i]) {
			return fmt.Errorf("%w: name contains invalid character", ErrInvalidMessageHeader)
		}
	}
	if len(value) > MaxMessageHeaderValueBytes {
		return fmt.Errorf("%w: value exceeds maximum length", ErrInvalidMessageHeader)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: value contains invalid UTF-8", ErrInvalidMessageHeader)
	}
	for i := 0; i < len(value); i++ {
		switch value[i] {
		case 0, '\r', '\n':
			return fmt.Errorf("%w: value contains invalid character", ErrInvalidMessageHeader)
		}
	}
	return nil
}

func isMessageHeaderNameByte(c byte) bool {
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

func (m Message) validated() (Message, error) {
	if err := ValidateMessage(m); err != nil {
		return Message{}, err
	}
	return m, nil
}

func validateMessageToken(kind, value string, maxBytes int) error {
	if value == "" {
		return fmt.Errorf("%w: %s must not be empty", ErrInvalidMessage, kind)
	}
	if len(value) > maxBytes {
		return fmt.Errorf("%w: %s exceeds maximum length", ErrInvalidMessage, kind)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s contains invalid UTF-8", ErrInvalidMessage, kind)
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return fmt.Errorf("%w: %s contains whitespace or control characters", ErrInvalidMessage, kind)
		}
	}
	return nil
}
