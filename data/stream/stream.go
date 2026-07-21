package stream

import (
	"context"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"
)

// ErrInvalidStream is returned when a stream producer/consumer method is
// invoked on a nil or otherwise uninitialized implementation.
var ErrInvalidStream = errors.New("stream: stream is not initialized")

// ErrInvalidName marks stream names that are unsafe as backend keys or labels.
var ErrInvalidName = errors.New("stream: invalid name")

// ErrInvalidPayload marks stream payloads that cannot safely be published.
var ErrInvalidPayload = errors.New("stream: invalid payload")

const (
	// MaxNameBytes caps stream names used as backend keys and metric labels.
	MaxNameBytes = 256
	// MaxPayloadFields caps the number of fields in a stream payload map.
	MaxPayloadFields = 64
	// MaxPayloadFieldNameBytes caps each payload field name.
	MaxPayloadFieldNameBytes = 128
	// MaxPayloadFieldValueBytes caps each payload field value.
	MaxPayloadFieldValueBytes = 64 * 1024
	// MaxTotalPayloadBytes caps aggregate name+value bytes across all fields.
	MaxTotalPayloadBytes = 1 << 20
)

// Message represents a stream event.
type Message struct {
	ID      string
	Stream  string
	Payload map[string]string
}

// Handler processes a stream message. Return nil to acknowledge.
type Handler func(ctx context.Context, msg Message) error

// Producer publishes messages to a stream.
// Implementations must reject stream names that fail [ValidateName] and
// payloads that fail [ValidatePayload].
type Producer interface {
	Produce(ctx context.Context, stream string, payload map[string]string) (string, error)
}

// Consumer reads messages from a stream with consumer group support.
type Consumer interface {
	// Consume blocks and processes messages until ctx is cancelled.
	// Fatal backend failures are currently logged by implementations and
	// surface as a return without error; a v3 signature will return error
	// so lifecycle runners can detect terminal exits (see V3_BREAKING_PROPOSALS).
	Consume(ctx context.Context, stream string, handler Handler)
}

// ValidateName checks that a stream name is safe for backend keys and metric
// labels: non-empty, bounded, valid UTF-8, and free of protocol/log-breaking
// whitespace or control bytes.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("%w: stream name must not be empty", ErrInvalidName)
	}
	if len(name) > MaxNameBytes {
		return fmt.Errorf("%w: name exceeds maximum length", ErrInvalidName)
	}
	if containsInvalidStringBytes(name) {
		return fmt.Errorf("%w: name contains invalid characters", ErrInvalidName)
	}
	return nil
}

// ValidatePayload checks stream payload maps against portable field-count,
// field-name/value size, and charset rules shared by Producer implementations.
func ValidatePayload(payload map[string]string) error {
	if payload == nil {
		return nil
	}
	if len(payload) > MaxPayloadFields {
		return fmt.Errorf("%w: field count exceeds maximum", ErrInvalidPayload)
	}
	total := 0
	for k, v := range payload {
		if k == "" {
			return fmt.Errorf("%w: field name must not be empty", ErrInvalidPayload)
		}
		if len(k) > MaxPayloadFieldNameBytes {
			return fmt.Errorf("%w: field name exceeds maximum length", ErrInvalidPayload)
		}
		if containsInvalidStringBytes(k) {
			return fmt.Errorf("%w: field name contains invalid characters", ErrInvalidPayload)
		}
		if len(v) > MaxPayloadFieldValueBytes {
			return fmt.Errorf("%w: field value exceeds maximum length", ErrInvalidPayload)
		}
		if !utf8.ValidString(v) {
			return fmt.Errorf("%w: field value is not valid UTF-8", ErrInvalidPayload)
		}
		total += len(k) + len(v)
		if total > MaxTotalPayloadBytes {
			return fmt.Errorf("%w: aggregate payload exceeds maximum size", ErrInvalidPayload)
		}
	}
	return nil
}

func containsInvalidStringBytes(s string) bool {
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
