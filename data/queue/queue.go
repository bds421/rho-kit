// asvs: V11.1.1, V11.1.2
package queue

import (
	"context"
	"errors"
	"fmt"
	"unicode"
	"unicode/utf8"
)

// ErrInvalidQueue is returned when a queue publisher/consumer method is
// invoked on a nil or otherwise uninitialized implementation.
var ErrInvalidQueue = errors.New("queue: queue is not initialized")

// ErrInvalidName marks queue names that are unsafe as backend keys, labels, or
// queue identifiers.
var ErrInvalidName = errors.New("queue: invalid name")

// ErrInvalidMessage marks queued jobs whose metadata is not portable across
// queue backends.
var ErrInvalidMessage = errors.New("queue: invalid message")

// ErrMessageTooLarge marks queued jobs rejected before they reach a backend
// because their payload exceeds the configured size policy.
var ErrMessageTooLarge = errors.New("queue: message exceeds max payload size")

// ErrBatchTooLarge marks batch enqueue calls rejected before they reach a
// backend because they contain too many messages.
var ErrBatchTooLarge = errors.New("queue: batch operation exceeds maximum message count")

// ErrDuplicateMessage marks an enqueue that was rejected because a message
// with the same ID is already present in the queue (FR-059 idempotency).
// Callers that treat a duplicate as success can errors.Is against this
// sentinel without importing a backend-specific package.
var ErrDuplicateMessage = errors.New("queue: duplicate message id")

const (
	// MaxNameBytes caps queue names used as backend keys and metric labels.
	MaxNameBytes = 256
	// MaxMessageIDBytes caps optional idempotency IDs.
	MaxMessageIDBytes = 255
	// MaxMessageTypeBytes caps job type names.
	MaxMessageTypeBytes = 256
	// DefaultMaxPayloadBytes is the safe default payload cap for queue backends.
	DefaultMaxPayloadBytes = 1 << 20
	// UnlimitedPayloadBytes disables the payload size check in [ValidateMessage].
	// Pass this explicit sentinel rather than 0 — zero means "use the default".
	UnlimitedPayloadBytes = -1
	// MaxBatchMessages caps batch enqueue operations so callers cannot build
	// unbounded backend command batches.
	MaxBatchMessages = 1024
)

// Message represents a queued job.
type Message struct {
	ID      string
	Type    string
	Payload []byte
}

// MessageTooLargeError reports the measured queue payload size and the
// configured limit.
type MessageTooLargeError struct {
	Size  int
	Limit int
}

func (e *MessageTooLargeError) Error() string {
	return fmt.Sprintf("queue: payload size %d exceeds max %d", e.Size, e.Limit)
}

func (e *MessageTooLargeError) Unwrap() error { return ErrMessageTooLarge }

// ValidateName checks that a queue name is safe for backend keys and metric
// labels: non-empty, bounded, valid UTF-8, and free of protocol/log-breaking
// whitespace or control bytes.
func ValidateName(name, kind string) error {
	if kind == "" {
		kind = "queue"
	}
	if name == "" {
		return fmt.Errorf("%w: %s name must not be empty", ErrInvalidName, kind)
	}
	if len(name) > MaxNameBytes {
		return fmt.Errorf("%w: name exceeds maximum length", ErrInvalidName)
	}
	if containsInvalidStringBytes(name) {
		return fmt.Errorf("%w: %s name contains invalid characters", ErrInvalidName, kind)
	}
	return nil
}

// ValidateMessage checks generic queue metadata and payload size before a
// backend persists the job.
//
// maxPayloadBytes semantics:
//   - 0: apply [DefaultMaxPayloadBytes] (fail-closed default; zero is the
//     natural unset config value and must not silently disable the cap)
//   - [UnlimitedPayloadBytes] (-1): disable the payload size check
//   - >0: enforce that exact limit
//   - any other negative value: invalid
func ValidateMessage(msg Message, maxPayloadBytes int) error {
	limit := maxPayloadBytes
	switch {
	case limit == 0:
		limit = DefaultMaxPayloadBytes
	case limit == UnlimitedPayloadBytes:
		limit = 0 // no payload size check below
	case limit < 0:
		return fmt.Errorf("%w: max payload bytes must be >= 0 or UnlimitedPayloadBytes", ErrInvalidMessage)
	}
	if msg.Type == "" {
		return fmt.Errorf("%w: type must not be empty", ErrInvalidMessage)
	}
	if len(msg.Type) > MaxMessageTypeBytes {
		return fmt.Errorf("%w: type exceeds maximum length", ErrInvalidMessage)
	}
	if containsInvalidStringBytes(msg.Type) {
		return fmt.Errorf("%w: type contains invalid characters", ErrInvalidMessage)
	}
	if len(msg.ID) > MaxMessageIDBytes {
		return fmt.Errorf("%w: id exceeds maximum length", ErrInvalidMessage)
	}
	if containsInvalidStringBytes(msg.ID) {
		return fmt.Errorf("%w: id contains invalid characters", ErrInvalidMessage)
	}
	if limit > 0 && len(msg.Payload) > limit {
		return &MessageTooLargeError{Size: len(msg.Payload), Limit: limit}
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

// Handler processes a queue message. Return nil to acknowledge,
// return an error to retry or dead-letter.
type Handler func(ctx context.Context, msg Message) error

// Publisher enqueues messages.
type Publisher interface {
	Enqueue(ctx context.Context, queue string, msg Message) error
}

// Consumer processes messages from a queue.
type Consumer interface {
	// Consume blocks and processes messages until ctx is cancelled.
	// Fatal backend failures are currently logged by implementations and
	// surface as a return without error; a v3 signature will return error
	// so lifecycle runners can detect terminal exits (see V3_BREAKING_PROPOSALS).
	Consume(ctx context.Context, queue string, handler Handler)
}
